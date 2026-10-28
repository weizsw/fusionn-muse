//go:build linux || darwin

package toolrun

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup gives the command its own process group and makes
// cancellation end that group, so a tool that starts a helper process cannot
// leave the helper running and holding the output pipes while the direct child
// is stopped. Only processes that stay in the group the command leads are ended:
// a descendant that creates its own session or its own group is outside this
// guarantee.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
}

// killProcessGroup signals the group the command leads, which is the command's
// own PID because it was started with Setpgid. The caller's process group is
// never signaled. A group that has already ended reports os.ErrProcessDone, the
// same answer the default cancellation gives, so Wait can tell "we stopped it"
// from "it had already finished by itself" and does not report cancellation for
// a run that failed on its own.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
