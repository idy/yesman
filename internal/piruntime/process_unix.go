//go:build !windows

package piruntime

import (
	"os/exec"
	"syscall"
)

func isolateInstaller(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killInstaller(cmd *exec.Cmd) {
	if cmd.Process != nil {
		if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
			_ = cmd.Process.Kill()
		}
	}
}
