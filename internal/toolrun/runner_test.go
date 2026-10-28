package toolrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// countingWriter counts the bytes a stream produced without retaining them, so
// a multi-megabyte line can be asserted with bounded test memory.
type countingWriter struct{ bytes int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	return len(p), nil
}

func TestExecRunnerStreamWritesToTheCallersWriters(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := (ExecRunner{}).Stream(context.Background(), "sh",
		&stdout, &stderr, "-c", "printf out; printf err >&2"); err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if stdout.String() != "out" {
		t.Fatalf("stdout = %q, want out", stdout.String())
	}
	if stderr.String() != "err" {
		t.Fatalf("stderr = %q, want err", stderr.String())
	}
}

func TestExecRunnerStreamDoesNotDecideWhatToPrint(t *testing.T) {
	output := captureStderr(t, func() {
		var stdout, stderr bytes.Buffer
		if err := (ExecRunner{}).Stream(context.Background(), "sh", &stdout, &stderr,
			"-c", "printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2"); err != nil {
			t.Fatalf("Stream returned error: %v", err)
		}
	})
	if output != "" {
		t.Fatalf("Stream printed %q; the caller decides what to display", output)
	}
}

func TestExecRunnerStreamDrainsWithoutLineLimits(t *testing.T) {
	// One line far beyond any line-buffer limit, then more bytes with no
	// trailing newline: the drain must not stop and must not drop a byte.
	const longBytes = 2 << 20
	var stdout countingWriter
	if err := (ExecRunner{}).Stream(context.Background(), "sh", &stdout, io.Discard,
		"-c", "head -c "+strconv.Itoa(longBytes)+" /dev/zero | tr '\\0' a; printf tail"); err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if stdout.bytes != int64(longBytes+len("tail")) {
		t.Fatalf("stdout bytes = %d, want %d", stdout.bytes, longBytes+len("tail"))
	}
}

func TestExecRunnerStreamKeepsCarriageReturnsAndUnsplitUTF8(t *testing.T) {
	var stdout bytes.Buffer
	if err := (ExecRunner{}).Stream(context.Background(), "sh", &stdout, io.Discard, "-c",
		`printf 'progress 1\rprogress 2\r'; printf '\344'; printf '\270'; printf '\255'`); err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	want := "progress 1\rprogress 2\r中"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestExecRunnerStreamKeepsTheRealExitStatus(t *testing.T) {
	var stdout bytes.Buffer
	err := (ExecRunner{}).Stream(context.Background(), "sh", &stdout, io.Discard, "-c", "printf out; exit 3")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want an *exec.ExitError", err)
	}
	if code := exitErr.ExitCode(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if stdout.String() != "out" {
		t.Fatalf("stdout = %q, want the bytes written before the exit", stdout.String())
	}
}

func TestExecRunnerRunKeepsExitStatusWithoutReplayingOutput(t *testing.T) {
	err := (ExecRunner{}).Run(context.Background(), "sh", "-c",
		`printf '{{SECRET_pf2gjx3a}}'; printf 'subtitle body text' >&2; exit 4`)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want an *exec.ExitError", err)
	}
	if code := exitErr.ExitCode(); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
	for _, leaked := range []string{"super-secret-api-key", "subtitle body text", "-c"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error %q replays %q", err, leaked)
		}
	}
}

func TestExecRunnerRunNamesTheCommandThatCouldNotStart(t *testing.T) {
	err := (ExecRunner{}).Run(context.Background(), "fusionn-muse-no-such-tool")
	if err == nil {
		t.Fatal("Run returned nil error for a missing tool")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("error = %v, want exec.ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "fusionn-muse-no-such-tool") {
		t.Fatalf("error = %q, want the safe command name", err)
	}
}

func TestExecRunnerOutputKeepsStdoutAndHidesStderr(t *testing.T) {
	out, err := (ExecRunner{}).Output(context.Background(), "sh", "-c",
		`printf out; printf '{{SECRET_pf2gjx3a}}' >&2; exit 5`)
	if string(out) != "out" {
		t.Fatalf("output = %q, want out", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 {
		t.Fatalf("error = %v, want exit status 5", err)
	}
	for _, leaked := range []string{"super-secret-api-key"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error %q replays %q", err, leaked)
		}
	}
}

func TestExecRunnerOutputDoesNotTruncateTheMachineResult(t *testing.T) {
	const resultBytes = 2 << 20
	out, err := (ExecRunner{}).Output(context.Background(), "sh", "-c",
		"head -c "+strconv.Itoa(resultBytes)+" /dev/zero | tr '\\0' a")
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if len(out) != resultBytes {
		t.Fatalf("output bytes = %d, want %d", len(out), resultBytes)
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
