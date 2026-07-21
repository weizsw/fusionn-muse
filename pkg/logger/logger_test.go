package logger

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestJobPrefix(t *testing.T) {
	ctx := WithJob(context.Background(), "d4e79fec-d17d-48bd-82e0-c064c6bc80e1")
	if got, want := Prefix(ctx), "[job_id=d4e79fec-d17d-48bd-82e0-c064c6bc80e1] "; got != want {
		t.Fatalf("Prefix() = %q, want %q", got, want)
	}

	ctx = WithAttempt(ctx, 2)
	if got, want := Prefix(ctx), "[job_id=d4e79fec-d17d-48bd-82e0-c064c6bc80e1 attempt=2] "; got != want {
		t.Fatalf("Prefix() = %q, want %q", got, want)
	}
}

func TestContextLoggerPrefixesEveryMessageLine(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	original := Log
	Log = zap.New(core).Sugar()
	defer func() { Log = original }()

	ctx := WithAttempt(WithJob(context.Background(), "job-a"), 2)
	FromContext(ctx).Errorf("first\nsecond")

	entries := observed.All()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for i, want := range []string{
		"[job_id=job-a attempt=2] first",
		"[job_id=job-a attempt=2] second",
	} {
		if entries[i].Message != want {
			t.Fatalf("entry %d = %q, want %q", i, entries[i].Message, want)
		}
	}
}

func TestJobContextsDoNotLeak(t *testing.T) {
	const runs = 100
	start := make(chan struct{})
	var wg sync.WaitGroup

	for _, jobID := range []string{"job-a", "job-b"} {
		jobID := jobID
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := WithAttempt(WithJob(context.Background(), jobID), 1)
			<-start
			for range runs {
				if got, want := Prefix(ctx), "[job_id="+jobID+" attempt=1] "; got != want {
					t.Errorf("Prefix() = %q, want %q", got, want)
				}
			}
		}()
	}

	close(start)
	wg.Wait()
}
