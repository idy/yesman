//go:build windows

package piruntime

import (
	"os/exec"
	"strconv"
)

func isolateInstaller(cmd *exec.Cmd) {}
func killInstaller(cmd *exec.Cmd) {
	if cmd.Process != nil {
		if exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run() != nil {
			_ = cmd.Process.Kill()
		}
	}
}
