package toolrun

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fusionn-muse/pkg/logger"
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

func TestExecRunnerOutputPreservesStderrOnFailure(t *testing.T) {
	_, err := (ExecRunner{}).Output(context.Background(), "sh", "-c", "printf 'err-1\\nerr-2\\n' >&2; exit 1")
	if err == nil {
		t.Fatal("Output returned nil error")
	}
	for _, line := range []string{"err-1", "err-2"} {
		if !strings.Contains(err.Error(), line) {
			t.Fatalf("error = %q, want %q", err, line)
		}
	}
}

func TestExecRunnerStreamPrefixesEveryOutputLine(t *testing.T) {
	ctx := logger.WithAttempt(logger.WithJob(context.Background(), "job-a"), 2)
	output := captureStderr(t, func() {
		_, _, err := (ExecRunner{}).Stream(ctx, "sh", "-c", "printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2")
		if err != nil {
			t.Fatalf("Stream returned error: %v", err)
		}
	})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 4 {
		t.Fatalf("output lines = %d, want 4; output = %q", len(lines), output)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "job-a ") {
			t.Fatalf("line lacks exact correlation prefix: %q", line)
		}
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = original }()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(output)
}
