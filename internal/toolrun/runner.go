package toolrun

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/fusionn-muse/pkg/logger"
)

const (
	dimStart = "\033[2m"
	dimEnd   = "\033[0m"
)

// Runner executes external tools.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (ExecRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (ExecRunner) Stream(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", "", fmt.Errorf("stderr pipe: %w", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go StreamDimmed(&wg, stdoutPipe, &stdoutBuf)
	go StreamDimmed(&wg, stderrPipe, &stderrBuf)

	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start %s: %w", name, err)
	}

	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return stdoutBuf.String(), stderrBuf.String(), err
	}
	return stdoutBuf.String(), stderrBuf.String(), nil
}

// StreamDimmed reads from r, writes to buf, and prints dimmed to stderr.
func StreamDimmed(wg *sync.WaitGroup, r io.Reader, buf *bytes.Buffer) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		fmt.Fprintf(os.Stderr, "%s  │ %s%s\n", dimStart, line, dimEnd)
	}

	if err := scanner.Err(); err != nil {
		logger.Debugf("Scanner error (may be normal): %v", err)
	}
}
