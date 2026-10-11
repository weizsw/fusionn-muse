package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/mediaintake"
)

func TestTaskListCountsOneMediaAndUnresolvedFailure(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("same media"), 0644); err != nil {
		t.Fatal(err)
	}
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.profile.Store("settings must not appear in list")
	p.prepare = func(_ context.Context, input *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		if input.SourcePath == "/missing.iso" {
			return nil, nil, errors.New("input unavailable")
		}
		return &mediaintake.ResolvedMedia{SourcePath: media, FileName: "movie.mp4", StagingPath: filepath.Join(root, "staging", "movie.mp4")}, nil, nil
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	var jobID string
	for range 2 {
		submission, err := q.Submit(context.Background(), media, "movie", "video")
		if err != nil {
			t.Fatal(err)
		}
		jobID = waitSubmission(t, q, submission.ID, SubmissionResolved).JobID
	}
	waitForStatus(t, q, jobID, StatusCompleted)
	failed, err := q.Submit(context.Background(), "/missing.iso", "missing", "video")
	if err != nil {
		t.Fatal(err)
	}
	waitSubmission(t, q, failed.ID, SubmissionFailed)
	page, err := q.ListTasks(context.Background(), TaskFilter{Limit: 1})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != failed.ID || page.Tasks[0].Status != "failed" || page.NextCursor == "" {
		t.Fatalf("first page=%+v, %v", page, err)
	}
	page, err = q.ListTasks(context.Background(), TaskFilter{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != jobID || page.Tasks[0].Kind != "job" || page.NextCursor != "" {
		t.Fatalf("second page=%+v, %v", page, err)
	}
	stats, err := q.TaskStats(context.Background(), TaskFilter{})
	if err != nil || stats.Total != 2 || stats.Statuses["completed"] != 1 || stats.Statuses["failed"] != 1 || stats.Statuses["interrupted"] != 0 {
		t.Fatalf("stats=%+v, %v", stats, err)
	}
}

func TestTaskListExposesFailedRetranslationWithoutLosingCompletedResult(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "movie.srt")
	if err := os.WriteFile(source, []byte("original source"), 0644); err != nil {
		t.Fatal(err)
	}
	q := New(processorFunc(func(ctx context.Context, job *Job) error {
		if job.AttemptKind == AttemptRetranslate {
			if err := job.BeginStage(ctx, StageTranslating); err != nil {
				return err
			}
			return errors.New("translation API rejected request")
		}
		return nil
	}), 1, 0)
	q.Start()
	defer q.Close()
	job := NewJob("completed", "/missing/movie.mp4", "movie.mp4", "", "")
	job.SubtitlePath = source
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, job.ID, StatusCompleted)
	if err := q.Retranslate(job.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, err := q.ListTasks(context.Background(), TaskFilter{Status: "failed"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Tasks) == 1 {
			if page.Tasks[0].JobStatus != StatusCompleted || page.Tasks[0].Error != "translation API rejected request" || page.Tasks[0].Stage != StageTranslating {
				t.Fatalf("failure projection=%+v", page.Tasks[0])
			}
			stats, err := q.TaskStats(context.Background(), TaskFilter{})
			if err != nil || stats.Total != 1 || stats.Statuses["failed"] != 1 || stats.Statuses["completed"] != 0 {
				t.Fatalf("stats=%+v, %v", stats, err)
			}
			if q.GetJob(job.ID).Status != StatusCompleted {
				t.Fatal("retained result was changed")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("failed retranslation was hidden")
}

func TestTaskQueriesRejectBadFiltersAndReportStorageFailure(t *testing.T) {
	q := New(processorFunc(func(context.Context, *Job) error { return nil }), 1, 0)
	q.Start()
	for _, filter := range []TaskFilter{{Status: "resolved"}, {Kind: "unknown"}, {Cursor: "bad"}} {
		if _, err := q.ListTasks(context.Background(), filter); !errors.Is(err, ErrInvalidTaskQuery) {
			t.Fatalf("filter=%+v, error=%v", filter, err)
		}
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ListTasks(context.Background(), TaskFilter{}); err == nil || errors.Is(err, ErrInvalidTaskQuery) {
		t.Fatalf("closed storage list error=%v", err)
	}
	if _, err := q.TaskStats(context.Background(), TaskFilter{}); err == nil {
		t.Fatal("closed storage returned zero counts as success")
	}
}

func TestTaskDetailPaginatesLinkedPreparationAndAttemptHistory(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.profile.Store("saved configuration")
	p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return &mediaintake.ResolvedMedia{SourcePath: media, FileName: "movie.mp4", StagingPath: filepath.Join(root, "staging", "movie.mp4")}, nil, nil
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	var receipts []*Submission
	for range 2 {
		input, err := q.Submit(context.Background(), media, "original name", "video")
		if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, waitSubmission(t, q, input.ID, SubmissionResolved))
	}
	waitForStatus(t, q, receipts[0].JobID, StatusCompleted)
	cursor := ""
	seen := map[string]int{}
	for range 3 {
		detail, err := q.GetTaskDetail(context.Background(), "job", receipts[0].JobID, cursor, 1)
		if err != nil || len(detail.History) != 1 || detail.Job == nil || detail.Status != "completed" {
			t.Fatalf("detail=%+v, %v", detail, err)
		}
		entry := detail.History[0]
		seen[entry.Kind]++
		if entry.SettingsSnapshot != "saved configuration" {
			t.Fatalf("history lost configuration: %+v", entry)
		}
		if entry.Kind == "preparation" && (entry.SubmissionID == "" || entry.SourcePath != media || entry.TorrentName != "original name") {
			t.Fatalf("history lost immutable submission: %+v", entry)
		}
		cursor = detail.NextHistoryCursor
	}
	if cursor != "" || seen["preparation"] != 2 || seen["attempt"] != 1 {
		t.Fatalf("history=%v cursor=%q", seen, cursor)
	}
	for _, receipt := range receipts {
		detail, err := q.GetTaskDetail(context.Background(), "submission", receipt.ID, "", 50)
		if err != nil || detail.Submission == nil || detail.Submission.Status != SubmissionResolved || detail.CanonicalTask == nil || detail.CanonicalTask.ID != receipts[0].JobID {
			t.Fatalf("resolved receipt detail=%+v, %v", detail, err)
		}
	}
	if _, err := q.GetTaskDetail(context.Background(), "job", "absent", "", 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing detail error=%v", err)
	}
}

func TestTaskCursorKeepsTiedRowsAndRejectsChangedFilter(t *testing.T) {
	q := New(processorFunc(func(context.Context, *Job) error { return nil }), 1, 0)
	q.Start()
	defer q.Close()
	created := time.Now().Add(-time.Minute)
	for _, id := range []string{"a", "b", "c"} {
		job := NewJob(id, "/missing/"+id+".mp4", id+".mp4", "", "")
		job.CreatedAt = created
		if err := q.Accept(job); err != nil {
			t.Fatal(err)
		}
	}
	first, err := q.ListTasks(context.Background(), TaskFilter{Kind: "job", Limit: 2})
	if err != nil || len(first.Tasks) != 2 || first.Tasks[0].ID != "c" || first.Tasks[1].ID != "b" || first.NextCursor == "" {
		t.Fatalf("tied first page=%+v, %v", first, err)
	}
	second, err := q.ListTasks(context.Background(), TaskFilter{Kind: "job", Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Tasks) != 1 || second.Tasks[0].ID != "a" || second.NextCursor != "" {
		t.Fatalf("tied second page=%+v, %v", second, err)
	}
	if _, err := q.ListTasks(context.Background(), TaskFilter{Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidTaskQuery) {
		t.Fatalf("changed filter accepted=%v", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), "settings_snapshot") || strings.Contains(string(encoded), "history") {
		t.Fatalf("heavy list=%s, %v", encoded, err)
	}
}

func TestTaskRetryOfFailedRetranslationRetainsItsSettingsAndCompletedResult(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "movie.srt")
	if err := os.WriteFile(source, []byte("original source"), 0644); err != nil {
		t.Fatal(err)
	}
	var translations atomic.Int32
	resumed := make(chan *Job, 1)
	p := &submissionTestProcessor{}
	p.profile.Store("original configuration")
	p.processorFunc = func(ctx context.Context, job *Job) error {
		if job.AttemptKind != AttemptRetranslate {
			return nil
		}
		if translations.Add(1) == 1 {
			if err := job.BeginStage(ctx, StageTranslating); err != nil {
				return err
			}
			return errors.New("upstream failure")
		}
		copy := *job
		resumed <- &copy
		<-ctx.Done()
		return ctx.Err()
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	job := NewJob("completed", "/missing/movie.mp4", "movie.mp4", "", "")
	job.SubtitlePath = source
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, job.ID, StatusCompleted)
	if err := q.Retranslate(job.ID); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, q, "job", job.ID, "failed")
	p.profile.Store("current configuration")
	result, err := q.ExecuteTaskAction(context.Background(), "job", job.ID, TaskAction{Action: "retry"})
	if err != nil || result.Attempt == nil || result.Attempt.SettingsSnapshot != "original configuration" {
		t.Fatalf("retry=%+v, %v", result, err)
	}
	select {
	case got := <-resumed:
		if got.Status != StatusCompleted || got.AttemptKind != AttemptRetranslate || got.StartStage != StageTranslating || got.SettingsSnapshot != "original configuration" {
			t.Fatalf("resumed=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("retry did not run")
	}
}

func waitTaskStatus(t *testing.T, q *Queue, kind, id, status string) *TaskDetail {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := q.GetTaskDetail(context.Background(), kind, id, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Status == status {
			return detail
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s/%s did not reach %s", kind, id, status)
	return nil
}

func TestTaskActionsReuseOnlyMatchingActiveRequest(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.srt")
	if err := os.WriteFile(source, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	running := make(chan struct{}, 1)
	p := &submissionTestProcessor{}
	p.profile.Store("initial")
	p.processorFunc = func(ctx context.Context, job *Job) error {
		if job.AttemptKind == AttemptRetranslate {
			running <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	job := NewJob("one", "/missing/movie.mp4", "movie.mp4", "", "")
	job.SubtitlePath = source
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, job.ID, StatusCompleted)
	p.profile.Store("selected")
	request := TaskAction{Action: "retranslate", UseCurrentSettings: true}
	first, err := q.ExecuteTaskAction(context.Background(), "job", job.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("attempt not running")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := q.ExecuteTaskAction(context.Background(), "job", job.ID, request)
			if err != nil || result.Attempt.ID != first.Attempt.ID {
				t.Errorf("duplicate=%+v %v", result, err)
			}
		}()
	}
	wg.Wait()
	if _, err := q.ExecuteTaskAction(context.Background(), "job", job.ID, TaskAction{Action: "retranslate"}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("different intent=%v", err)
	}
	p.profile.Store("changed again")
	if _, err := q.ExecuteTaskAction(context.Background(), "job", job.ID, request); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("different profile=%v", err)
	}
	detail, err := q.GetTaskDetail(context.Background(), "job", job.ID, "", 50)
	if err != nil || len(detail.History) != 2 || detail.History[0].SettingsSnapshot != "selected" || detail.History[0].Request == nil || !detail.History[0].Request.UseCurrentSettings {
		t.Fatalf("history=%+v %v", detail, err)
	}
}

func TestTaskStatisticsShareListFilters(t *testing.T) {
	q := New(&submissionTestProcessor{}, 1, 0)
	defer q.Close()
	for _, name := range []string{"one", "two"} {
		if err := q.store.insertSubmission(context.Background(), &Submission{ID: name, SourcePath: "/" + name}, "snapshot"); err != nil {
			t.Fatal(err)
		}
	}
	for _, filter := range []TaskFilter{{Kind: "submission"}, {Status: "pending"}, {Kind: "job"}, {Status: "failed"}, {Kind: "submission", Status: "pending"}} {
		page, err := q.ListTasks(context.Background(), filter)
		if err != nil {
			t.Fatal(err)
		}
		stats, err := q.TaskStats(context.Background(), filter)
		if err != nil || stats.Total != len(page.Tasks) {
			t.Fatalf("filter=%+v stats=%+v list=%+v err=%v", filter, stats, page, err)
		}
	}
	if _, err := q.TaskStats(context.Background(), TaskFilter{Kind: "bad"}); !errors.Is(err, ErrInvalidTaskQuery) {
		t.Fatalf("invalid stats filter=%v", err)
	}
}

func TestTaskMetadataMigrationFromVersionFourPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	q, err := Open(path, &submissionTestProcessor{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	job := NewJob("old-job", "/missing/movie.mp4", "movie.mp4", "", "")
	job.SettingsSnapshot = "original configuration"
	if err := q.store.insertJob(job); err != nil {
		t.Fatal(err)
	}
	if err := q.store.insertSubmission(context.Background(), &Submission{ID: "old-receipt", SourcePath: "/missing/input"}, "original preparation"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`ALTER TABLE attempts DROP COLUMN request_meta; PRAGMA user_version=4;`); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = Open(path, &submissionTestProcessor{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	var version int
	if err := q.store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("version=%d %v", version, err)
	}
	for _, old := range []struct{ kind, id, settings string }{{"job", "old-job", "original configuration"}, {"submission", "old-receipt", "original preparation"}} {
		detail, err := q.GetTaskDetail(context.Background(), old.kind, old.id, "", 50)
		if err != nil || len(detail.History) != 1 || detail.History[0].SettingsSnapshot != old.settings || detail.History[0].Request != nil {
			t.Fatalf("migrated %s=%+v %v", old.kind, detail, err)
		}
	}
}
