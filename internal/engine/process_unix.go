//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// prepareCommand sets up a new process group for the subprocess so all child
// processes can be terminated together on cancellation.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
