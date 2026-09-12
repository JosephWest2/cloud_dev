//go:build !linux

package access

import (
	"io"
	"os/exec"
)

func foregroundShell(cmd *exec.Cmd, stdin io.Reader, group int) func() { return func() {} }
