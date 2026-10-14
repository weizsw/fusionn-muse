package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fusionn-muse/internal/mediaintake"
)

func seedBulkJob(t *testing.T, q *Queue, id, status string) *Job {
	t.Helper()
	job := NewJob(id, "/missing/"+id+".mp4", id+".mp4", "", "")
	job.SettingsSnapshot = "saved-" + id
	if err := q.store.insertJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE attempts SET status=? WHERE id=?`, status, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE jobs SET status=? WHERE id=?`, status, job.ID); err != nil {
		t.Fatal(err)
	}
	return job
}

func seedBulkSubmission(t *testing.T, q *Queue, id, status string) *Submission {
	t.Helper()
	input := &Submission{ID: id, SourcePath: "/missing/" + id + ".iso"}
	if err := q.store.insertSubmission(context.Background(), input, "saved-"+id); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE submission_runs SET status=? WHERE id=?`, status, input.RunID); err != nil {
		t.Fatal(err)
	}
	return input
}

func newBulkTestProcessor() *submissionTestProcessor {
	return &submissionTestProcessor{
		processorFunc: func(ctx context.Context, _ *Job) error {
			<-ctx.Done()
			return ctx.Err()
		},
		prepare: func(ctx context.Context, _ *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	}
}

func TestBulkTaskActionsRecoverAllMatchingTasksBeyondOnePage(t *testing.T) {
	for _, action := range []string{"retry", "resume"} {
		t.Run(action, func(t *testing.T) {
			status := "failed"
			if action == "resume" {
				status = "interrupted"
			}
			q := New(newBulkTestProcessor(), 1, 0)
			defer q.Close()
			for i := range 205 {
				seedBulkJob(t, q, fmt.Sprintf("target-%03d", i), status)
			}
			source := filepath.Join(t.TempDir(), "source.srt")
			if err := os.WriteFile(source, []byte("source subtitles"), 0644); err != nil {
				t.Fatal(err)
			}
			seedBulkJob(t, q, "retranslation", status)
			if _, err := q.store.db.Exec(`UPDATE jobs SET status='completed',subtitle_path=? WHERE id='retranslation'`, source); err != nil {
				t.Fatal(err)
			}
			if _, err := q.store.db.Exec(`UPDATE attempts SET kind=? WHERE job_id='retranslation'`, AttemptRetranslate); err != nil {
				t.Fatal(err)
			}
			seedBulkSubmission(t, q, "target-submission", status)
			for _, other := range []string{"pending", "processing", "completed", "failed", "interrupted"} {
				if other != status {
					seedBulkJob(t, q, "other-"+other, other)
				}
			}
			for _, other := range []string{"pending", "preparing", "ignored", "failed", "interrupted"} {
				if other != status {
					seedBulkSubmission(t, q, "other-submission-"+other, other)
				}
			}
			receipt := seedBulkSubmission(t, q, "resolved-receipt", "resolved")
			if _, err := q.store.db.Exec(`UPDATE submission_runs SET job_id='retranslation' WHERE id=?`, receipt.RunID); err != nil {
				t.Fatal(err)
			}
			q.Start()
			result, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: action})
			if err != nil || result.Matched != 207 || result.Accepted != 207 || result.Failed != 0 || len(result.Results) != 207 {
				t.Fatalf("bulk result=%+v error=%v", result, err)
			}
			seen := map[TaskReference]bool{}
			for _, item := range result.Results {
				if seen[item.TaskReference] || item.ExecutionID == 0 || item.Error != "" {
					t.Fatalf("duplicate or unsuccessful result=%+v", item)
				}
				seen[item.TaskReference] = true
				wantHistory := 2
				if item.ID == "retranslation" {
					wantHistory++ // Includes the resolved Submission's preparation history.
				}
				detail, err := q.GetTaskDetail(context.Background(), item.Kind, item.ID, "", 50)
				if err != nil || len(detail.History) != wantHistory || detail.History[0].SettingsSnapshot != "saved-"+item.ID {
					t.Fatalf("lost history or settings for %s/%s: %+v error=%v", item.Kind, item.ID, detail, err)
				}
				if item.ID == "retranslation" && (detail.Job.Status != StatusCompleted || detail.History[0].ExecutionKind != string(AttemptRetranslate)) {
					t.Fatalf("lost retained completed result: %+v", detail)
				}
			}
			for _, other := range []string{"pending", "processing", "completed", "failed", "interrupted"} {
				if other == status {
					continue
				}
				attempts, err := q.store.attempts("other-" + other)
				if err != nil || len(attempts) != 1 {
					t.Fatalf("nonmatching job changed: %+v error=%v", attempts, err)
				}
			}
			for _, other := range []string{"pending", "preparing", "ignored", "failed", "interrupted", "resolved"} {
				if other == status {
					continue
				}
				id := "other-submission-" + other
				if other == "resolved" {
					id = "resolved-receipt"
				}
				runs, err := q.store.submissionRuns(id)
				if err != nil || len(runs) != 1 {
					t.Fatalf("nonmatching submission changed: %+v error=%v", runs, err)
				}
			}
		})
	}
}

type bulkSnapshotProcessor struct {
	*submissionTestProcessor
	snapshots atomic.Int32
	err       error
}

func (p *bulkSnapshotProcessor) SnapshotSettings() (string, error) {
	return fmt.Sprintf("current-%d", p.snapshots.Add(1)), p.err
}

func TestBulkTaskActionsSnapshotCurrentSettingsOnceBeforeAnyRecovery(t *testing.T) {
	for _, snapshotError := range []error{nil, errors.New("configuration unavailable")} {
		t.Run(fmt.Sprint(snapshotError), func(t *testing.T) {
			p := &bulkSnapshotProcessor{submissionTestProcessor: newBulkTestProcessor(), err: snapshotError}
			q := New(p, 1, 0)
			defer q.Close()
			seedBulkJob(t, q, "job-a", "failed")
			seedBulkJob(t, q, "job-b", "failed")
			seedBulkSubmission(t, q, "input", "failed")
			q.Start()
			result, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: "retry", UseCurrentSettings: true})
			if p.snapshots.Load() != 1 {
				t.Fatalf("snapshot calls=%d, want one", p.snapshots.Load())
			}
			if snapshotError != nil {
				if !errors.Is(err, snapshotError) || result != nil {
					t.Fatalf("snapshot failure=%+v error=%v", result, err)
				}
				stats, err := q.TaskStats(context.Background(), TaskFilter{Status: "failed"})
				if err != nil || stats.Total != 3 {
					t.Fatalf("snapshot failure admitted work: %+v error=%v", stats, err)
				}
				return
			}
			if err != nil || result.Accepted != 3 || result.Failed != 0 {
				t.Fatalf("bulk result=%+v error=%v", result, err)
			}
			for _, item := range result.Results {
				detail, err := q.GetTaskDetail(context.Background(), item.Kind, item.ID, "", 50)
				if err != nil || len(detail.History) != 2 || detail.History[0].SettingsSnapshot != "current-1" {
					t.Fatalf("inconsistent batch configuration: %+v error=%v", detail, err)
				}
				if item.Kind == "job" && (detail.Job.TranslationCacheEnabled || !detail.History[0].Request.UseCurrentSettings) {
					t.Fatalf("current settings lost action semantics: %+v", detail.History[0])
				}
			}
		})
	}
}

func TestBulkTaskActionsContinueAfterIndividualFailures(t *testing.T) {
	q := New(newBulkTestProcessor(), 1, 0)
	defer q.Close()
	seedBulkJob(t, q, "accepted-job", "failed")
	seedBulkJob(t, q, "blocked-job", "failed")
	seedBulkSubmission(t, q, "accepted-input", "failed")
	seedBulkSubmission(t, q, "blocked-input", "failed")
	if _, err := q.store.db.Exec(`CREATE TRIGGER reject_bulk_attempt BEFORE INSERT ON attempts
		WHEN NEW.job_id='blocked-job' BEGIN SELECT RAISE(FAIL, 'attempt unavailable'); END;
		CREATE TRIGGER reject_bulk_preparation BEFORE INSERT ON submission_runs
		WHEN NEW.submission_id='blocked-input' BEGIN SELECT RAISE(FAIL, 'preparation unavailable'); END;`); err != nil {
		t.Fatal(err)
	}
	q.Start()
	result, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: "retry"})
	if err != nil || result.Matched != 4 || result.Accepted != 2 || result.Failed != 2 || len(result.Results) != 4 {
		t.Fatalf("partial result=%+v error=%v", result, err)
	}
	for _, item := range result.Results {
		blocked := strings.HasPrefix(item.ID, "blocked-")
		if blocked != (item.Error != "") || blocked == (item.ExecutionID != 0) {
			t.Fatalf("unreported outcome=%+v", item)
		}
		detail, err := q.GetTaskDetail(context.Background(), item.Kind, item.ID, "", 50)
		wantHistory := 2
		if blocked {
			wantHistory = 1
		}
		if err != nil || len(detail.History) != wantHistory {
			t.Fatalf("partial result not committed independently: %+v error=%v", detail, err)
		}
	}
}

func TestBulkTaskSelectionCannotRecoverALaterExecution(t *testing.T) {
	q := New(newBulkTestProcessor(), 1, 0)
	defer q.Close()
	job := seedBulkJob(t, q, "job", "failed")
	input := seedBulkSubmission(t, q, "input", "failed")
	request := TaskAction{Action: "retry"}
	first, err := q.store.createTaskAction(context.Background(), job.ID, request, nil, nil, job.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE attempts SET status='failed' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.createTaskAction(context.Background(), job.ID, request, nil, nil, job.AttemptID); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("bulk retry recovered later failed Attempt: %v", err)
	}
	run, err := q.store.recoverSubmission(context.Background(), input.ID, nil, "retry", input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE submission_runs SET status='failed' WHERE id=?`, run.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.recoverSubmission(context.Background(), input.ID, nil, "retry", input.RunID); !errors.Is(err, ErrInvalidSubmissionAction) {
		t.Fatalf("bulk retry recovered later failed Preparation run: %v", err)
	}
	for _, ref := range []TaskReference{{Kind: "job", ID: job.ID}, {Kind: "submission", ID: input.ID}} {
		detail, err := q.GetTaskDetail(context.Background(), ref.Kind, ref.ID, "", 50)
		if err != nil || len(detail.History) != 2 {
			t.Fatalf("stale selection changed history: %+v error=%v", detail, err)
		}
	}
}

func TestBulkTaskConcurrentRequestsDoNotDuplicateActiveExecutions(t *testing.T) {
	q := New(newBulkTestProcessor(), 1, 0)
	defer q.Close()
	seedBulkJob(t, q, "job", "interrupted")
	seedBulkSubmission(t, q, "input", "interrupted")
	q.Start()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: "resume"})
			if err != nil || result.Matched != result.Accepted+result.Failed {
				t.Errorf("bulk result=%+v error=%v", result, err)
			}
		}()
	}
	wg.Wait()
	for _, ref := range []TaskReference{{Kind: "job", ID: "job"}, {Kind: "submission", ID: "input"}} {
		detail, err := q.GetTaskDetail(context.Background(), ref.Kind, ref.ID, "", 50)
		if err != nil || len(detail.History) != 2 {
			t.Fatalf("concurrent requests duplicated recovery: %+v error=%v", detail, err)
		}
	}
}

func TestBulkTaskActionsValidateRequestsAndReportEmptyOrUnavailableQueue(t *testing.T) {
	q := New(newBulkTestProcessor(), 1, 0)
	defer q.Close()
	for _, action := range []string{"", "retranslate", "cancel"} {
		if _, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: action}); !errors.Is(err, ErrInvalidTaskAction) {
			t.Fatalf("invalid action %q accepted: %v", action, err)
		}
	}
	if _, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: "retry"}); !errors.Is(err, ErrQueueNotRunning) {
		t.Fatalf("unstarted queue=%v", err)
	}
	q.Start()
	for _, action := range []string{"retry", "resume"} {
		result, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: action})
		if err != nil || result.Matched != 0 || result.Accepted != 0 || result.Failed != 0 || result.Results == nil {
			t.Fatalf("empty bulk result=%+v error=%v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.ExecuteBulkTaskAction(ctx, BulkTaskAction{Action: "retry"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bulk request=%v", err)
	}
	q.Stop()
	if _, err := q.ExecuteBulkTaskAction(context.Background(), BulkTaskAction{Action: "resume"}); !errors.Is(err, ErrQueueNotRunning) {
		t.Fatalf("stopped queue=%v", err)
	}
}
