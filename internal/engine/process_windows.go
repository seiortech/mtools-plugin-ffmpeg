//go:build windows

package engine

import (
	"os/exec"
	"strconv"
)

// prepareCommand configures process tree termination for Windows.
func prepareCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		killCmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
		return killCmd.Run()
	}
}
