//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts pi in a group of its own, so that stopping it
// also stops the subagents and commands it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
