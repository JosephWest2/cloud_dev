//go:build !linux

package setup

import "os/exec"

func isolateProcess(cmd *exec.Cmd, isolate bool) func() { return func() {} }
