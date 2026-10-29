package queue

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/pkg/logger"
)

type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLog) lines(event string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var found []string
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(line, "event="+event+" ") {
			found = append(found, line)
		}
	}
	return found
}

func captureQueueLogs(t *testing.T) *lockedLog {
	t.Helper()
	t.Setenv("LOG_LEVEL", "")
	buf := &lockedLog{}
	logger.Configure(logger.Config{Output: zapcore.AddSync(buf)})
	t.Cleanup(func() { logger.Configure(logger.Config{Output: zapcore.AddSync(&lockedLog{})}) })
	return buf
}

// requirePanicReport checks that exactly one line reports the panic with its
// stack.
func requirePanicReport(t *testing.T, lines []string, panicText string) {
	t.Helper()
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], `error="panic: `+panicText+`"`) || !strings.Contains(lines[0], ` stack="`) {
		t.Errorf("line lacks the panic or its stack: %s", lines[0])
	}
}

// A panic in Job processing fails the attempt through the normal failure path
// instead of ending the process, and the queue keeps running.
func TestProcessorPanicFailsAttempt(t *testing.T) {
	logs := captureQueueLogs(t)
	q := New(processorFunc(func(_ context.Context, job *Job) error {
		if job.ID == "boom" {
			panic("process bug")
		}
		return nil
	}), 0, 0)
	q.Start()
	defer q.Stop()

	for _, id := range []string{"boom", "after"} {
		job := NewJob(id, "/tmp/"+id+".mp4", id+".mp4", "", "")
		job.IsLight = true
		if err := q.Accept(job); err != nil {
			t.Fatalf("accept %s: %v", id, err)
		}
	}
	waitForStatus(t, q, "boom", StatusFailed)
	waitForStatus(t, q, "after", StatusCompleted)
	q.Stop()

	requirePanicReport(t, logs.lines("job_failed"), "process bug")
}

// A panicking staged attempt is retried like any translation failure; the
// retry warning and the final failure both carry the stack.
func TestStagedPanicIsRetriedThenFails(t *testing.T) {
	logs := captureQueueLogs(t)
	q := New(stageProcessorFunc(func(ctx context.Context, job *Job) (Stage, error) {
		if err := job.BeginStage(ctx, job.AttemptStage); err != nil {
			return "", err
		}
		panic("stage bug")
	}), 2, 0)
	job := NewJob("boom", "/tmp/boom.mp4", "boom.mp4", "", "")
	if err := q.store.insertJob(job); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := q.store.db.Exec(`UPDATE attempts SET start_stage=?, current_stage='' WHERE job_id=?`, StageTranslating, job.ID); err != nil {
		t.Fatalf("queue for translation: %v", err)
	}
	if _, err := q.store.db.Exec(`UPDATE jobs SET checkpoint=? WHERE id=?`, StageTranscribed, job.ID); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	q.Start()
	defer q.Stop()

	waitForStatus(t, q, job.ID, StatusFailed)
	q.Stop()

	requirePanicReport(t, logs.lines("translation_retry"), "stage bug")
	requirePanicReport(t, logs.lines("job_failed"), "stage bug")
}

// A panic while preparing a Submission fails that Submission instead of ending
// the process.
func TestSubmissionPreparationPanicFailsSubmission(t *testing.T) {
	logs := captureQueueLogs(t)
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		panic("intake bug")
	}
	q := New(p, 0, 0)
	q.Start()
	defer q.Close()

	receipt, err := q.Submit(context.Background(), t.TempDir(), "movie", "")
	if err != nil {
		t.Fatal(err)
	}
	waitSubmission(t, q, receipt.ID, SubmissionFailed)
	q.Stop()

	requirePanicReport(t, logs.lines("submission_failed"), "intake bug")
}
