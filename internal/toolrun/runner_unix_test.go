//go:build linux || darwin

package toolrun

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The cancellation regression needs a tool that really forks: a plain
// "sh -c sleep" is unreliable, because a shell that execs its last command hides
// the bug (the process that must be stopped is then the direct child itself).
// This fixture forks a child that inherits both output pipes and outlives the
// direct child, exactly as a tool that starts a helper process does. The child
// appends a tick to a heartbeat file, so a test can prove whether it is still
// executing without depending on process IDs, whose liveness a reaped zombie or a
// reused ID makes unreliable. It also records its own PID, which the test uses
// only to clean up a survivor of a failing run: "$1" is the heartbeat file and
// "$2" the PID file.
const heartbeatScript = `while :; do echo tick >> "$1"; sleep 0.1; done & echo $! > "$2"; wait`

// toolCall runs one ExecRunner method against the fixture's arguments.
type toolCall func(ctx context.Context, args ...string) error

func streamCall(ctx context.Context, args ...string) error {
	return (ExecRunner{}).Stream(ctx, "sh", io.Discard, io.Discard, args...)
}

func runCall(ctx context.Context, args ...string) error {
	return (ExecRunner{}).Run(ctx, "sh", args...)
}

func outputCall(ctx context.Context, args ...string) error {
	_, err := (ExecRunner{}).Output(ctx, "sh", args...)
	return err
}

// startHeartbeatTool starts the fixture in the background and returns the
// heartbeat file, the PID file and the channel the call reports on. A cleanup
// ends a forked child that outlived a failing run, so no live orphan is left
// behind.
func startHeartbeatTool(ctx context.Context, t *testing.T, call toolCall) (heartbeat string, done <-chan error) {
	t.Helper()
	directory := t.TempDir()
	heartbeat = filepath.Join(directory, "heartbeat")
	pidFile := filepath.Join(directory, "child.pid")

	result := make(chan error, 1)
	go func() {
		result <- call(ctx, "-c", heartbeatScript, "sh", heartbeat, pidFile)
	}()

	t.Cleanup(func() { endRecordedChild(t, pidFile) })
	return heartbeat, result
}

// endRecordedChild kills a forked child that is still alive, which only happens
// when a run failed and left it behind. The PID is never used to decide whether
// the child stopped: a reaped zombie can keep answering a signal, so the tests
// use the heartbeat for that.
func endRecordedChild(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Logf("could not end the forked child %d: %v", pid, err)
	}
}

// TestKillProcessGroupReportsNothingToKillAsProcessDone pins the Cancel
// convention: a group that has already ended answers os.ErrProcessDone, not nil,
// so Wait does not report a cancellation for a run that had finished by itself.
func TestKillProcessGroupReportsNothingToKillAsProcessDone(t *testing.T) {
	if err := killProcessGroup(&exec.Cmd{}); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killProcessGroup before the process started = %v, want os.ErrProcessDone", err)
	}
}

// waitForHeartbeat waits until the forked child has written a tick, which proves
// it started and is executing before the test cancels.
func waitForHeartbeat(t *testing.T, file string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if info, err := os.Stat(file); err == nil && info.Size() > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the forked child never wrote its heartbeat")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertHeartbeatStopped proves the forked child is no longer executing: after a
// settle window, the file must not grow any further.
func assertHeartbeatStopped(t *testing.T, file string) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	first := heartbeatSize(t, file)
	time.Sleep(time.Second)
	second := heartbeatSize(t, file)
	if first != second {
		t.Fatalf("the forked child is still running: heartbeat grew from %d to %d bytes", first, second)
	}
}

func heartbeatSize(t *testing.T, file string) int64 {
	t.Helper()
	info, err := os.Stat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("stat heartbeat: %v", err)
	}
	return info.Size()
}

// TestExecRunnerStreamStopsOnContextCancel keeps the original deadline and
// return bound, and additionally checks that the tool's forked child stopped
// executing: a child that survives the cancellation keeps ticking.
func TestExecRunnerStreamStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	heartbeat, done := startHeartbeatTool(ctx, t, streamCall)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Stream returned nil error after the context deadline")
		}
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("ctx error = %v, want deadline exceeded", ctx.Err())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not return after the context deadline")
	}
	assertHeartbeatStopped(t, heartbeat)
}

// TestExecRunnerStopsTheForkedToolTreeOnCancel cancels only after the forked
// child has proved it is running, so a child that survives cannot pass, and it
// covers every method because they share the command constructor.
func TestExecRunnerStopsTheForkedToolTreeOnCancel(t *testing.T) {
	for _, testCase := range []struct {
		name string
		call toolCall
	}{
		{"Stream", streamCall},
		{"Run", runCall},
		{"Output", outputCall},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			heartbeat, done := startHeartbeatTool(ctx, t, testCase.call)

			waitForHeartbeat(t, heartbeat)
			cancel()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("the canceled call returned nil error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the call did not return within 5s after the context was canceled")
			}
			assertHeartbeatStopped(t, heartbeat)
		})
	}
}
