package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/mediaintake"
)

type submissionTestProcessor struct {
	processorFunc
	prepare func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error)
	profile atomic.Value
}

func (p *submissionTestProcessor) PrepareSubmission(ctx context.Context, input *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
	return p.prepare(ctx, input)
}

func (p *submissionTestProcessor) SnapshotSettings() (string, error) {
	if value := p.profile.Load(); value != nil {
		return value.(string), nil
	}
	return "", nil
}

func waitSubmission(t *testing.T, q *Queue, id string, status SubmissionStatus) *Submission {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		submission, err := q.GetSubmission(id)
		if err != nil {
			t.Fatal(err)
		}
		if submission.Status == status {
			return submission
		}
		time.Sleep(5 * time.Millisecond)
	}
	submission, err := q.GetSubmission(id)
	t.Fatalf("submission = %+v, %v; want %s", submission, err, status)
	return nil
}

func TestSubmitAcknowledgesWhilePreparationIsBlocked(t *testing.T) {
	started, release := make(chan *Submission, 1), make(chan struct{})
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error {
		t.Error("unresolved input reached Job processing")
		return nil
	}}
	p.profile.Store("original")
	p.prepare = func(ctx context.Context, input *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		started <- input
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-release:
			return nil, nil, os.ErrNotExist
		}
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	receipt, err := q.Submit(context.Background(), "/missing/movie.iso", "movie", "video")
	if err != nil || receipt.ID == "" || receipt.Status != SubmissionPending {
		t.Fatalf("receipt = %+v, %v", receipt, err)
	}
	select {
	case input := <-started:
		if input.ID != receipt.ID || input.SettingsSnapshot != "original" {
			t.Fatalf("prepared input = %+v", input)
		}
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	close(release)
	failed := waitSubmission(t, q, receipt.ID, SubmissionFailed)
	if failed.JobID != "" || failed.Error == "" || len(q.GetAllJobs()) != 0 {
		t.Fatalf("lost preparation failure: %+v", failed)
	}
}

func TestShutdownInterruptsPreparationAndRecoveryUsesFrozenSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	started := make(chan *Submission, 2)
	makeProcessor := func(profile string) *submissionTestProcessor {
		p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
		p.profile.Store(profile)
		p.prepare = func(ctx context.Context, input *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
			started <- input
			<-ctx.Done()
			return nil, nil, errors.New("signal: killed")
		}
		return p
	}
	q, err := Open(path, makeProcessor("original"), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	receipt, err := q.Submit(context.Background(), "/movie.iso", "movie", "video")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = Open(path, makeProcessor("current"), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Close()
	waitSubmission(t, q, receipt.ID, SubmissionInterrupted)
	if _, err := q.RecoverSubmission(context.Background(), receipt.ID, false); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-started:
		if input.SettingsSnapshot != "original" || input.RunID == receipt.RunID {
			t.Fatalf("recovered input = %+v", input)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not start")
	}
}

func TestSubmissionResultPersistenceRetryDoesNotRepeatPreparation(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("ready media"), 0644); err != nil {
		t.Fatal(err)
	}
	var preparations atomic.Int32
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		preparations.Add(1)
		return &mediaintake.ResolvedMedia{SourcePath: media, FileName: "movie.mp4", StagingPath: filepath.Join(root, "staging", "movie.mp4")}, nil, nil
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	if _, err := q.store.db.Exec(`CREATE TRIGGER reject_submission_result BEFORE UPDATE ON submission_runs
		WHEN NEW.status='resolved' BEGIN SELECT RAISE(FAIL, 'temporary result failure'); END`); err != nil {
		t.Fatal(err)
	}
	receipt, err := q.Submit(context.Background(), media, "movie", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for preparations.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	if preparations.Load() != 1 || len(q.GetAllJobs()) != 0 {
		t.Fatalf("preparations = %d, jobs = %d", preparations.Load(), len(q.GetAllJobs()))
	}
	if _, err := q.store.db.Exec(`DROP TRIGGER reject_submission_result`); err != nil {
		t.Fatal(err)
	}
	resolved := waitSubmission(t, q, receipt.ID, SubmissionResolved)
	if preparations.Load() != 1 || resolved.JobID == "" || len(q.GetAllJobs()) != 1 {
		t.Fatalf("result = %+v, preparations = %d", resolved, preparations.Load())
	}
}

func TestSubmissionDuplicateMediaDoesNotCreateAnotherAttempt(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("same media"), 0644); err != nil {
		t.Fatal(err)
	}
	var cleanups atomic.Int32
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.profile.Store("original")
	p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return &mediaintake.ResolvedMedia{SourcePath: media, FileName: "movie.mp4", StagingPath: filepath.Join(root, "staging", "movie.mp4")}, func() error { cleanups.Add(1); return nil }, nil
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	first, err := q.Submit(context.Background(), media, "movie", "")
	if err != nil {
		t.Fatal(err)
	}
	first = waitSubmission(t, q, first.ID, SubmissionResolved)
	p.profile.Store("current")
	duplicate, err := q.Submit(context.Background(), media, "renamed", "")
	if err != nil {
		t.Fatal(err)
	}
	duplicate = waitSubmission(t, q, duplicate.ID, SubmissionResolved)
	if duplicate.JobID != first.JobID || len(q.GetAllJobs()) != 1 {
		t.Fatalf("duplicate = %+v, first = %+v", duplicate, first)
	}
	detail := q.GetJobDetail(first.JobID)
	if len(detail.Attempts) != 1 || detail.Attempts[0].SettingsSnapshot != "original" {
		t.Fatalf("duplicate changed job: %+v", detail)
	}
	deadline := time.Now().Add(time.Second)
	for cleanups.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if cleanups.Load() != 2 {
		t.Fatalf("cleanup calls = %d; want completed preparation and duplicate released", cleanups.Load())
	}
	for _, preserved := range []string{media, filepath.Join(root, "staging", "movie.mp4")} {
		if _, err := os.Stat(preserved); err != nil {
			t.Fatalf("cleanup lost input or admitted media: %s: %v", preserved, err)
		}
	}
}

func TestIgnoredSubmissionRecoverySelectsSettingsExplicitly(t *testing.T) {
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.profile.Store("original")
	p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return nil, nil, mediaintake.ErrNoValidMedia
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	receipt, err := q.Submit(context.Background(), "/downloads/folder", "movie", "video")
	if err != nil {
		t.Fatal(err)
	}
	waitSubmission(t, q, receipt.ID, SubmissionIgnored)
	p.profile.Store("current")
	if _, err := q.RecoverSubmission(context.Background(), receipt.ID, true); err != nil {
		t.Fatal(err)
	}
	waitSubmission(t, q, receipt.ID, SubmissionIgnored)
	if _, err := q.RecoverSubmission(context.Background(), receipt.ID, false); err != nil {
		t.Fatal(err)
	}
	waitSubmission(t, q, receipt.ID, SubmissionIgnored)
	detail, err := q.GetSubmissionDetail(context.Background(), receipt.ID)
	if err != nil || len(detail.Runs) != 3 || detail.Runs[0].SettingsSnapshot != "original" || detail.Runs[1].SettingsSnapshot != "current" || detail.Runs[2].SettingsSnapshot != "current" {
		t.Fatalf("recovery history = %+v, %v", detail, err)
	}
	if detail.RunID != detail.Runs[len(detail.Runs)-1].ID {
		t.Fatal("detail did not return a coherent current run")
	}
}

func TestSubmissionAdmissionConflictIsDiscoverableWithoutOverwritingMedia(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source.mp4"), filepath.Join(root, "staging", "movie.mp4")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new media"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("other media"), 0644); err != nil {
		t.Fatal(err)
	}
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error {
		t.Error("conflicting media reached processing")
		return nil
	}}
	p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return &mediaintake.ResolvedMedia{SourcePath: source, FileName: "movie.mp4", StagingPath: target}, nil, nil
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	receipt, err := q.Submit(context.Background(), source, "movie", "")
	if err != nil {
		t.Fatal(err)
	}
	resolved := waitSubmission(t, q, receipt.ID, SubmissionResolved)
	deadline := time.Now().Add(time.Second)
	for q.GetJob(resolved.JobID).Status != StatusFailed && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	job := q.GetJob(resolved.JobID)
	if job.Status != StatusFailed || job.FailureStage != StagePreparing || job.Error == "" {
		t.Fatalf("admission failure = %+v", job)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "other media" {
		t.Fatalf("target overwritten: %q, %v", data, err)
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "new media" {
		t.Fatalf("source modified: %q, %v", data, err)
	}
}

func TestPreparedSourceCacheCleanupRequiresDurableAdmission(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "conflict"}[conflict], func(t *testing.T) {
			root := t.TempDir()
			cache := filepath.Join(root, "cache", "movie.mp4")
			staging := filepath.Join(root, "staging", "movie.mp4")
			if err := os.MkdirAll(filepath.Dir(cache), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, []byte("prepared media"), 0644); err != nil {
				t.Fatal(err)
			}
			if conflict {
				if err := os.MkdirAll(filepath.Dir(staging), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(staging, []byte("unrelated media"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			cleaned := make(chan struct{})
			p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error {
				if conflict {
					t.Error("conflicting media reached processing")
				}
				return nil
			}}
			p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
				return &mediaintake.ResolvedMedia{SourcePath: cache, StagingPath: staging, FileName: "movie.mp4"}, func() error {
					defer close(cleaned)
					return os.Remove(cache)
				}, nil
			}
			q := New(p, 1, 0)
			q.Start()
			defer q.Close()
			receipt, err := q.Submit(context.Background(), "/original/image.iso", "movie", "")
			if err != nil {
				t.Fatal(err)
			}
			resolved := waitSubmission(t, q, receipt.ID, SubmissionResolved)
			if conflict {
				waitForStatus(t, q, resolved.JobID, StatusFailed)
				if _, err := os.Stat(cache); err != nil {
					t.Fatalf("failed admission lost recovery cache: %v", err)
				}
				data, err := os.ReadFile(staging)
				if err != nil || string(data) != "unrelated media" {
					t.Fatalf("overwrote conflict: %q, %v", data, err)
				}
			} else {
				select {
				case <-cleaned:
				case <-time.After(time.Second):
					t.Fatal("successful admission leaked preparation cache")
				}
				data, err := os.ReadFile(staging)
				if err != nil || string(data) != "prepared media" {
					t.Fatalf("cleanup damaged admitted media: %q, %v", data, err)
				}
			}
		})
	}
}
