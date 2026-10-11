package executor

import (
	"context"

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

func (r *fakeRunner) Stream(_ context.Context, name string, args ...string) (string, string, error) {
	r.streamCalls = append(r.streamCalls, commandCall{name: name, args: append([]string(nil), args...)})
	if r.onStream != nil {
		return r.onStream(name, args...)
	}
	return "", "", nil
}
