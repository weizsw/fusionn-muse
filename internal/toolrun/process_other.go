//go:build !linux && !darwin

package toolrun

import "os/exec"

// configureProcessGroup keeps the default cancellation on platforms the tool
// executor does not run on, so this package still builds there. It adds no
// behavior: the executor targets Linux and macOS.
func configureProcessGroup(*exec.Cmd) {}
