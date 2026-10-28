package executor

import (
	"context"
	"io"

	"github.com/fusionn-muse/pkg/logger"
)

func init() {
	logger.Init(true)
}

type commandCall struct {
	name string
	args []string
}

type fakeRunner struct {
	streamCalls []commandCall
	onStream    func(name string, args ...string) (string, string, error)
}

func (*fakeRunner) Run(context.Context, string, ...string) error { return nil }

func (*fakeRunner) Output(context.Context, string, ...string) ([]byte, error) { return nil, nil }

// Stream writes what the fake captured into the writers the caller provided,
// so the real consumer path is exercised.
func (r *fakeRunner) Stream(_ context.Context, name string, stdout, stderr io.Writer, args ...string) error {
	r.streamCalls = append(r.streamCalls, commandCall{name: name, args: append([]string(nil), args...)})
	if r.onStream == nil {
		return nil
	}
	capturedStdout, capturedStderr, err := r.onStream(name, args...)
	if stdout != nil && capturedStdout != "" {
		_, _ = io.WriteString(stdout, capturedStdout)
	}
	if stderr != nil && capturedStderr != "" {
		_, _ = io.WriteString(stderr, capturedStderr)
	}
	return err
}
