package queue

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/toolrun"
)

// failTool runs a tool that writes an error line to stderr and fails.
func failTool(ctx context.Context, line string) error {
	return toolrun.ExecRunner{}.Run(ctx, "sh", "-c", "echo 'progress 10%'; echo '"+line+"' >&2; exit 1")
}

// A tool failure that reaches the Job boundary shows the tool's error lines on
// job_failed, so the cause shows without DEBUG.
func TestJobFailedCarriesToolDetail(t *testing.T) {
	logs := captureQueueLogs(t)
	q := New(processorFunc(func(ctx context.Context, _ *Job) error {
		return fmt.Errorf("hard-sub OCR: %w", failTool(ctx, "RuntimeError: model missing"))
	}), 0, 0)
	q.Start()
	defer q.Stop()

	job := NewJob("tool", "/tmp/tool.mp4", "tool.mp4", "", "")
	job.IsLight = true
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept: %v", err)
	}
	waitForStatus(t, q, job.ID, StatusFailed)
	q.Stop()

	lines := logs.lines("job_failed")
	if len(lines) != 1 || !strings.Contains(lines[0], ` detail="RuntimeError: model missing" `) {
		t.Fatalf("job_failed lines = %q, want the tool detail", lines)
	}
	if strings.Contains(lines[0], "progress") {
		t.Errorf("standard output reached the failure line: %s", lines[0])
	}
}

func TestSubmissionFailedCarriesToolDetail(t *testing.T) {
	logs := captureQueueLogs(t)
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.prepare = func(ctx context.Context, _ *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return nil, nil, fmt.Errorf("remux copy failed: %w", failTool(ctx, "Conversion failed!"))
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

	lines := logs.lines("submission_failed")
	if len(lines) != 1 || !strings.Contains(lines[0], ` detail="Conversion failed!" `) {
		t.Fatalf("submission_failed lines = %q, want the tool detail", lines)
	}
}
