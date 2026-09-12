//go:build linux

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

func ioctl(fd uintptr, request uintptr, pointer unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(pointer))
	if e != 0 {
		return e
	}
	return nil
}

// Move the supervised worker into the foreground when stdin is a controlling
// terminal. OpenSSH's raw terminal then carries Ctrl-C as remote input. Restore
// both foreground ownership and termios even after forced process cleanup.
func foreground(cmd *exec.Cmd) func() {
	var term syscall.Termios
	var group int32
	if ioctl(os.Stdin.Fd(), syscall.TCGETS, unsafe.Pointer(&term)) != nil || ioctl(os.Stdin.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&group)) != nil {
		return func() {}
	}
	signal.Ignore(syscall.SIGTTOU)
	cmd.SysProcAttr.Foreground = true
	cmd.SysProcAttr.Ctty = int(os.Stdin.Fd())
	return func() {
		ioctl(os.Stdin.Fd(), syscall.TIOCSPGRP, unsafe.Pointer(&group))
		ioctl(os.Stdin.Fd(), syscall.TCSETS, unsafe.Pointer(&term))
		signal.Reset(syscall.SIGTTOU)
	}
}
