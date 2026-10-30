// Package toolrun executes external tools. It knows process execution and
// stream draining only: which events matter, what is safe to show and what
// counts as a business failure belong to the caller.
package toolrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// pipeWaitDelay bounds what Wait may spend on the output pipes once the context
// is canceled or the direct child has exited. It is the backstop for a tool that
// leaves a descendant holding a pipe; stopping those processes is the job of the
// cancellation path, not of this bound.
const pipeWaitDelay = 3 * time.Second

// stderrLimit bounds the standard error that Run keeps for a failure
// diagnostic.
const stderrLimit = 32 << 10

// newCommand builds the one command shape every method uses, so Run, Output and
// Stream share the same cancellation and pipe-wait behavior.
func newCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configureProcessGroup(cmd)
	cmd.WaitDelay = pipeWaitDelay
	return cmd
}

// Runner executes external tools.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Stream runs the tool with writers the caller owns. Both pipes are copied
	// while the process runs, so no line length, carriage return or missing
	// final newline can stop the drain or block the child.
	Stream(ctx context.Context, name string, stdout, stderr io.Writer, args ...string) error
}

type ExecRunner struct{}

// Run executes the tool and discards its standard output, which is
// third-party text. A failed run keeps the end of the standard error on its
// *exec.ExitError, as Output does, so the caller can log a bounded diagnostic
// (see FailureOutput). The error text keeps the real exit status and a safe
// command name; it never carries the command line or the tool's output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := newCommand(ctx, name, args...)
	stderr := &tailBuffer{}
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.kept()
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Output returns the tool's standard output, which is the machine result the
// caller asked for, and does not truncate it. A failed run returns the exit
// status in the error; the tool's standard error stays out of the error text
// and is kept, bounded by os/exec, on the *exec.ExitError.
func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := newCommand(ctx, name, args...).Output()
	if err != nil {
		return output, fmt.Errorf("%s: %w", name, err)
	}
	return output, nil
}

// Stream runs the tool with the caller's writers. The caller decides what a
// reader sees and bounds what it retains; a nil writer discards that stream.
func (ExecRunner) Stream(ctx context.Context, name string, stdout, stderr io.Writer, args ...string) error {
	cmd := newCommand(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// FailureOutput returns the standard error that each failed Run or Output
// wrapped in err kept, in order, or "" when there is none. It is third-party
// text: log it only through a bounded, redacted field such as
// logger.FailureDetail.
func FailureOutput(err error) string {
	// errors.As stops at the first match, so an error that joins several runs
	// is read branch by branch.
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		var kept []string
		for _, inner := range joined.Unwrap() {
			if output := FailureOutput(inner); output != "" {
				kept = append(kept, output)
			}
		}
		return strings.Join(kept, "\n")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(exitErr.Stderr)
	}
	return ""
}

// tailBuffer keeps the last stderrLimit bytes written to it. os/exec writes
// each stream from one goroutine, so it needs no lock.
type tailBuffer struct {
	buf     []byte
	trimmed bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrLimit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-stderrLimit:]...)
		t.trimmed = true
	}
	return len(p), nil
}

// kept returns the retained text. When the head was dropped, the first,
// partial line goes too: it can start inside a credential that redaction no
// longer recognizes.
func (t *tailBuffer) kept() []byte {
	if !t.trimmed {
		return t.buf
	}
	if i := bytes.IndexByte(t.buf, '\n'); i >= 0 {
		return t.buf[i+1:]
	}
	return nil
}
