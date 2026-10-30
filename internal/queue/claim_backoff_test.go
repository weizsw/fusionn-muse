package queue

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/pkg/logger"
)

// Each failed claim is one WARN that names the wait before the next try. The
// wait doubles up to the cap, and a successful claim starts it over.
func TestClaimBackoffDoublesToTheCap(t *testing.T) {
	logs := captureQueueLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the wait returns at once; only the sequence is checked
	log := logger.Component("queue")
	cause := errors.New("disk I/O error")

	var backoff claimBackoff
	for range 7 {
		if backoff.failed(ctx, log, "Queued stage could not be claimed", cause) {
			t.Fatal("failed() must report a stopping queue")
		}
	}
	backoff.reset()
	backoff.failed(ctx, log, "Queued stage could not be claimed", cause)

	lines := logs.lines("claim_failed")
	want := []string{"1000", "2000", "4000", "8000", "16000", "30000", "30000", "1000"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), lines)
	}
	for i, line := range lines {
		if !strings.Contains(line, " WARN component=queue event=claim_failed ") ||
			!strings.Contains(line, " retry_after_ms="+want[i]+" ") || !strings.Contains(line, `error="disk I/O error"`) {
			t.Errorf("line %d = %s, want a WARN with retry_after_ms=%s", i, line, want[i])
		}
	}
}

// prodShaped has the interfaces of the production processor, so Start runs the
// Submission loop and the three stage lanes.
type prodShaped struct{}

func (prodShaped) Process(context.Context, *Job) error               { return nil }
func (prodShaped) ProcessStage(context.Context, *Job) (Stage, error) { return "", nil }
func (prodShaped) PrepareSubmission(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
	return nil, nil, mediaintake.ErrNoValidMedia
}

// When the database fails, each claim loop logs once per backoff step instead
// of every 100ms, and Stop still returns at once.
func TestClaimLoopsBackOffWhenTheDatabaseFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		processor Processor
		loops     int
	}{
		{"staged", prodShaped{}, 4},
		{"legacy", processorFunc(func(context.Context, *Job) error { return nil }), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureQueueLogs(t)
			q, err := Open(filepath.Join(t.TempDir(), "muse.db"), tc.processor, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			q.Start()
			time.Sleep(200 * time.Millisecond)
			_ = q.store.db.Close() // every claim now fails, as on a disk I/O error
			time.Sleep(1500 * time.Millisecond)

			// Each loop fails right away and again after its 1s wait; the
			// next try is 2s later.
			lines := logs.lines("claim_failed")
			if len(lines) < tc.loops || len(lines) > 2*tc.loops {
				t.Errorf("got %d claim_failed lines in 1.5s, want %d to %d", len(lines), tc.loops, 2*tc.loops)
			}
			for _, line := range lines {
				if !strings.Contains(line, " WARN ") || !strings.Contains(line, " retry_after_ms=") {
					t.Errorf("not a WARN with the wait: %s", line)
					break
				}
			}

			stopped := make(chan struct{})
			go func() { q.Stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("Stop waited for a claim backoff")
			}
		})
	}
}
