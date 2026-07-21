package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fusionn-muse/pkg/logger"
)

func init() {
	logger.Init(true)
}

type countingProcessor struct {
	mu       sync.Mutex
	calls    int
	err      error
	attempts []int
	jobIDs   []string
}

func newCountingProcessor(err error) *countingProcessor {
	return &countingProcessor{err: err}
}

func (p *countingProcessor) Process(ctx context.Context, _ *Job) error {
	p.mu.Lock()
	p.calls++
	p.attempts = append(p.attempts, logger.Attempt(ctx))
	p.jobIDs = append(p.jobIDs, logger.JobID(ctx))
	p.mu.Unlock()
	return p.err
}

func (p *countingProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestAutomaticRetriesKeepJobIDAndIncrementAttempt(t *testing.T) {
	proc := newCountingProcessor(errors.New("boom"))
	q := New(proc, 3, 1)
	q.Start()

	job := NewJob("job-a", "/tmp/source.mp4", "source.mp4", "", "")
	q.Enqueue(job)
	waitForCalls(t, proc, 3)
	q.Stop()
	if job.Status != StatusFailed {
		t.Fatalf("job status = %q, want %q", job.Status, StatusFailed)
	}

	proc.mu.Lock()
	defer proc.mu.Unlock()
	if got, want := proc.attempts, []int{1, 2, 3}; !equalInts(got, want) {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	for _, jobID := range proc.jobIDs {
		if jobID != "job-a" {
			t.Fatalf("job ID = %q, want job-a", jobID)
		}
	}
}

func TestRunImmediateFailureDoesNotRetry(t *testing.T) {
	proc := newCountingProcessor(errors.New("boom"))
	q := New(proc, 3, 1)
	job := NewJob("job1", "/tmp/source.mp4", "source.mp4", "", "")
	job.IsLight = true

	q.RunImmediate(job)

	waitForLightFailures(t, q, 1)

	if proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1", proc.callCount())
	}
	if job.Status != StatusFailed {
		t.Fatalf("job.Status = %q, want %q", job.Status, StatusFailed)
	}
	if q.GetQueueStats()["light_failed"] != 1 {
		t.Fatalf("light_failed = %d, want 1", q.GetQueueStats()["light_failed"])
	}
}

func waitForCalls(t *testing.T, processor *countingProcessor, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if processor.callCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("processor calls = %d, want %d", processor.callCount(), want)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func waitForLightFailures(t *testing.T, q *Queue, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.GetQueueStats()["light_failed"] == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("light_failed = %d, want %d", q.GetQueueStats()["light_failed"], want)
}
