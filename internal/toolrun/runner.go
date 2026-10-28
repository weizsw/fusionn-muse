// Package toolrun executes external tools. It knows process execution and
// stream draining only: which events matter, what is safe to show and what
// counts as a business failure belong to the caller.
package toolrun

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// pipeWaitDelay bounds what Wait may spend on the output pipes once the context
// is canceled or the direct child has exited. It is the backstop for a tool that
// leaves a descendant holding a pipe; stopping those processes is the job of the
// cancellation path, not of this bound.
const pipeWaitDelay = 3 * time.Second

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

// Run executes the tool and discards its streams, which are third-party text.
// The error keeps the real exit status and a safe command name; it never
// carries the command line or the tool's output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := newCommand(ctx, name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Output returns the tool's standard output, which is the machine result the
// caller asked for, and does not truncate it. A failed run returns the exit
// status in the error; the tool's standard error stays out of the error text.
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
