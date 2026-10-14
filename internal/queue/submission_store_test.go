package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSubmissionPersistsBeforePreparationAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	input := &Submission{ID: "submission", SourcePath: "/not-yet-readable/movie.iso", TorrentName: "movie", Category: "video"}
	if err := s.insertSubmission(context.Background(), input, `{"model":"original"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got, err := s.submission("submission")
	if err != nil || got.Status != SubmissionPending || got.JobID != "" || got.SourcePath != input.SourcePath || got.SettingsSnapshot != `{"model":"original"}` {
		t.Fatalf("persisted submission = %+v, %v", got, err)
	}
	claimed, err := s.claimSubmission(context.Background())
	if err != nil || claimed == nil || claimed.ID != input.ID || claimed.Status != SubmissionPreparing {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if again, err := s.claimSubmission(context.Background()); err != nil || again != nil {
		t.Fatalf("second claim = %+v, %v", again, err)
	}
}

func TestSubmissionRestartRequiresExplicitRecoveryAndPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.insertSubmission(context.Background(), &Submission{ID: "submission", SourcePath: "/movie.iso"}, "original"); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.claimSubmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got, err := s.submission("submission")
	if err != nil || got.Status != SubmissionInterrupted || got.Error == "" || got.RunID != claimed.RunID {
		t.Fatalf("interrupted submission = %+v, %v", got, err)
	}
	if pending, err := s.claimSubmission(context.Background()); err != nil || pending != nil {
		t.Fatalf("interrupted submission auto-started: %+v, %v", pending, err)
	}
	current := "current"
	recovered, err := s.recoverSubmission(context.Background(), "submission", &current, "", 0)
	if err != nil || recovered.Status != SubmissionPending || recovered.RunID == claimed.RunID || recovered.SettingsSnapshot != current {
		t.Fatalf("recovery = %+v, %v", recovered, err)
	}
	history, err := s.submissionRuns("submission")
	if err != nil || len(history) != 2 || history[0].Status != SubmissionInterrupted || history[0].SettingsSnapshot != "original" {
		t.Fatalf("history = %+v, %v", history, err)
	}
}

func TestSubmissionResolutionAtomicallyLinksOneJobForDuplicateMedia(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("same media"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for _, id := range []string{"first", "duplicate"} {
		if err := s.insertSubmission(context.Background(), &Submission{ID: id, SourcePath: media}, "frozen"); err != nil {
			t.Fatal(err)
		}
		submission, err := s.claimSubmission(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		job := NewJob(id, media, "movie.mp4", "", "")
		job.SettingsSnapshot = submission.SettingsSnapshot
		linked, err := s.resolveSubmission(context.Background(), submission, job)
		if err != nil || linked.ID != "first" {
			t.Fatalf("linked job = %+v, %v", linked, err)
		}
		got, err := s.submission(id)
		if err != nil || got.Status != SubmissionResolved || got.JobID != "first" {
			t.Fatalf("resolved submission = %+v, %v", got, err)
		}
	}
	jobs, err := s.list("")
	if err != nil || len(jobs) != 1 || jobs[0].SettingsSnapshot != "frozen" {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	attempts, err := s.attempts("first")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("duplicate created an Attempt: %+v, %v", attempts, err)
	}
}

func TestSubmissionResolutionRollsBackJobWhenLinkCannotPersist(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if err := s.insertSubmission(context.Background(), &Submission{ID: "submission", SourcePath: "/movie.mp4"}, "frozen"); err != nil {
		t.Fatal(err)
	}
	submission, err := s.claimSubmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_submission_result BEFORE UPDATE ON submission_runs
		WHEN NEW.status='resolved' BEGIN SELECT RAISE(FAIL, 'result write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolveSubmission(context.Background(), submission, NewJob("job", "/movie.mp4", "movie.mp4", "", "")); err == nil {
		t.Fatal("expected persistence failure")
	}
	if jobs, err := s.list(""); err != nil || len(jobs) != 0 {
		t.Fatalf("job escaped rollback: %+v, %v", jobs, err)
	}
	got, err := s.submission("submission")
	if err != nil || got.Status != SubmissionPreparing || got.JobID != "" {
		t.Fatalf("submission escaped rollback: %+v, %v", got, err)
	}
}

func TestSubmissionConcurrentRecoveryCreatesOnlyOneRun(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if err := s.insertSubmission(context.Background(), &Submission{ID: "submission", SourcePath: "/movie.mp4"}, "original"); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.claimSubmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishSubmission(context.Background(), claimed, SubmissionFailed, "preparation failed"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := s.recoverSubmission(context.Background(), "submission", nil, "", 0); err != nil || got.SettingsSnapshot != "original" {
				t.Errorf("recovery = %+v, %v", got, err)
			}
		}()
	}
	wg.Wait()
	runs, err := s.submissionRuns("submission")
	if err != nil || len(runs) != 2 || runs[0].Error != "preparation failed" || runs[1].Status != SubmissionPending {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	current := "different settings"
	if _, err := s.recoverSubmission(context.Background(), "submission", &current, "", 0); !errors.Is(err, ErrInvalidSubmissionAction) {
		t.Fatalf("conflicting recovery error = %v", err)
	}
}

func TestSubmissionMigrationPreservesVersionTwoJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	job := NewJob("legacy", "/downloads/movie.mp4", "movie.mp4", "torrent", "video")
	job.SettingsSnapshot = "legacy settings"
	if err := s.insertJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE submission_runs; DROP TABLE submissions; PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	jobs, err := s.list("")
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].SettingsSnapshot != job.SettingsSnapshot {
		t.Fatalf("migrated Jobs = %+v, %v", jobs, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM submissions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration invented submission history: %d, %v", count, err)
	}
	if err := s.insertSubmission(context.Background(), &Submission{ID: "new", SourcePath: "/new.iso"}, "new settings"); err != nil {
		t.Fatal(err)
	}
}

func TestSubmissionInsertRollsBackWhenInitialRunCannotPersist(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err := s.db.Exec(`CREATE TRIGGER reject_preparation BEFORE INSERT ON submission_runs BEGIN SELECT RAISE(FAIL, 'disk write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.insertSubmission(context.Background(), &Submission{ID: "submission", SourcePath: "/movie.iso"}, "original"); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := s.submission("submission"); !errors.Is(err, ErrSubmissionNotFound) {
		t.Fatalf("submission escaped rollback: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM submissions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphaned input: %d, %v", count, err)
	}
}

func TestSubmissionOutcomeWritesAreIdempotent(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for _, id := range []string{"failed", "resolved"} {
		if err := s.insertSubmission(context.Background(), &Submission{ID: id, SourcePath: "/movie.mp4"}, "original"); err != nil {
			t.Fatal(err)
		}
		input, err := s.claimSubmission(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if id == "failed" {
			for range 2 {
				if err := s.finishSubmission(context.Background(), input, SubmissionFailed, "failed"); err != nil {
					t.Fatal(err)
				}
			}
		} else {
			job := NewJob("job", "/movie.mp4", "movie.mp4", "", "")
			for range 2 {
				linked, err := s.resolveSubmission(context.Background(), input, job)
				if err != nil || linked.ID != job.ID || linked.AttemptID == 0 || linked.AttemptStatus != AttemptProcessing {
					t.Fatalf("idempotent resolution = %+v, %v", linked, err)
				}
			}
		}
	}
	jobs, err := s.list("")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
}
