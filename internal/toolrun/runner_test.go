package toolrun

import (
	"context"
	"strings"
	"testing"
)

func TestExecRunnerStreamCapturesOutput(t *testing.T) {
	runner := ExecRunner{}

	stdout, stderr, err := runner.Stream(context.Background(), "sh", "-c", "printf out; printf err >&2")
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if strings.TrimSpace(stdout) != "out" {
		t.Fatalf("stdout = %q, want out", stdout)
	}
	if strings.TrimSpace(stderr) != "err" {
		t.Fatalf("stderr = %q, want err", stderr)
	}
}
