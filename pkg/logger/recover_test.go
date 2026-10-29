package logger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// recoverFrom runs fn and returns its panic as an error, the way a worker
// boundary does.
func recoverFrom(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Recovered(r)
		}
	}()
	fn()
	return nil
}

// panicsHere stands in for a bug deep in a job.
func panicsHere() {
	panic("nil job state")
}

func TestRecoveredKeepsValueAndPanickingFrame(t *testing.T) {
	err := recoverFrom(panicsHere)
	var pe *PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("recovered error is %T, want *PanicError", err)
	}
	if got, want := err.Error(), "panic: nil job state"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !strings.Contains(pe.Stack, "panicsHere") {
		t.Errorf("stack lacks the panicking frame:\n%s", pe.Stack)
	}
}

func TestPanicStackOnlyForPanics(t *testing.T) {
	buf := capture(t, "")
	log := Component("worker")
	ctx := WithJobID(context.Background(), "j1")
	wrapped := fmt.Errorf("stage merge: %w", recoverFrom(panicsHere))
	plain := errors.New("plain failure")
	log.Error(ctx, "job_failed", "Job failed", zap.Error(wrapped), PanicStack(wrapped))
	log.Error(ctx, "job_failed", "Job failed", zap.Error(plain), PanicStack(plain))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], `error="stage merge: panic: nil job state"`) {
		t.Errorf("panic line lacks the error: %s", lines[0])
	}
	if !strings.Contains(lines[0], ` stack="`) || !strings.Contains(lines[0], "panicsHere") {
		t.Errorf("panic line lacks the recovered stack: %s", lines[0])
	}
	if strings.Contains(lines[1], "stack=") {
		t.Errorf("plain failure must not carry a stack: %s", lines[1])
	}
}

func TestRequestIDReader(t *testing.T) {
	if got := RequestID(context.Background()); got != "" {
		t.Errorf("RequestID without one = %q, want empty", got)
	}
	ctx := WithJobID(WithRequestID(context.Background(), "req-1"), "j1")
	if got := RequestID(ctx); got != "req-1" {
		t.Errorf("RequestID = %q, want req-1", got)
	}
}
