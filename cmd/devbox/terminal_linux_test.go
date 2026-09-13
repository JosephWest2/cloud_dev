//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestForegroundTerminalInterruptResizeAndRestore(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	var unlock int32
	if err = ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Fatal(err)
	}
	var number uint32
	if err = ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestForegroundTerminalHelper$")
	cmd.Env = append(os.Environ(), "DEVBOX_TTY_TEST_HELPER=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	data := make(chan []byte, 16)
	go func() {
		for {
			b := make([]byte, 4096)
			n, e := master.Read(b)
			if n > 0 {
				data <- b[:n]
			}
			if e != nil {
				close(data)
				return
			}
		}
	}()
	var collected bytes.Buffer
	expect := func(s string) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for !strings.Contains(collected.String(), s) {
			select {
			case b, ok := <-data:
				if !ok {
					t.Fatalf("PTY ended before %q: %s", s, &collected)
				}
				collected.Write(b)
			case <-timer.C:
				t.Fatalf("TTY stalled before %q: %s", s, &collected)
			}
		}
	}
	expect("READY")
	master.Write([]byte{3})
	expect("INTERRUPTED")
	size := [4]uint16{37, 101, 0, 0}
	if err = ioctl(master.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	master.Write([]byte("size\n"))
	expect("37 101")
	master.Write([]byte("exit\n"))
	expect("RESTORED")
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
func TestForegroundTerminalHelper(t *testing.T) {
	if os.Getenv("DEVBOX_TTY_TEST_HELPER") != "1" {
		return
	}
	var before syscall.Termios
	if err := ioctl(os.Stdin.Fd(), syscall.TCGETS, unsafe.Pointer(&before)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `trap 'echo INTERRUPTED' INT
stty -echo
echo READY
while :; do
  read -r action
  case "$action" in size) stty size;; exit) break;; esac
done
`)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	restore := foreground(cmd)
	if err := cmd.Run(); err != nil {
		restore()
		t.Fatal(err)
	}
	restore()
	var after syscall.Termios
	var group int32
	if ioctl(os.Stdin.Fd(), syscall.TCGETS, unsafe.Pointer(&after)) != nil || before != after {
		t.Fatal("terminal mode not restored")
	}
	if ioctl(os.Stdin.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&group)) != nil || int(group) != syscall.Getpgrp() {
		t.Fatal("foreground owner not restored")
	}
	fmt.Fprintln(os.Stdout, "RESTORED")
}
