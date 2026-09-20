//go:build linux

package setup

import (
	"os/exec"
	"syscall"
)

func isolateProcess(cmd *exec.Cmd, isolate bool) func() {
	if !isolate {
		return func() {}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	return func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}
