//go:build linux

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

func downInputIsTerminal() bool {
	var term syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&term)))
	return err == 0
}
