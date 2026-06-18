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
	mu    sync.Mutex
	calls int
	err   error
}

func newCountingProcessor(err error) *countingProcessor {
	return &countingProcessor{err: err}
}

func (p *countingProcessor) Process(context.Context, *Job) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.err
}

func (p *countingProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
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
