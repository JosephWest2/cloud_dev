//go:build linux

package access

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

func terminalIOCTL(fd uintptr, request uintptr, pointer unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(pointer))
	if e != 0 {
		return e
	}
	return nil
}

// The multiplexed client passes its terminal descriptors to the SSH master,
// which reads keyboard input. Both must belong to the foreground process group.
// Hand off only when launching the authenticated client, preserving setup's
// existing foreground owner and cancellation behavior.
func foregroundShell(cmd *exec.Cmd, stdin io.Reader, group int) func() {
	terminal, ok := stdin.(*os.File)
	if !ok {
		return func() {}
	}
	var term syscall.Termios
	var previous int32
	if terminalIOCTL(terminal.Fd(), syscall.TCGETS, unsafe.Pointer(&term)) != nil || terminalIOCTL(terminal.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&previous)) != nil {
		return func() {}
	}
	ignored := signal.Ignored(syscall.SIGTTOU)
	signal.Ignore(syscall.SIGTTOU)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group, Foreground: true, Ctty: int(terminal.Fd())}
	return func() {
		terminalIOCTL(terminal.Fd(), syscall.TIOCSPGRP, unsafe.Pointer(&previous))
		terminalIOCTL(terminal.Fd(), syscall.TCSETS, unsafe.Pointer(&term))
		if !ignored {
			signal.Reset(syscall.SIGTTOU)
		}
	}
}
