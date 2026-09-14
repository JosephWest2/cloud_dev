//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
	t.Cleanup(func() { master.Close() })
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
	t.Cleanup(func() { slave.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestForegroundTerminalHelper$")
	cmd.Env = append(os.Environ(), "DEVBOX_TTY_TEST_HELPER=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		// The session helper cancels and reaps its exact child before exiting.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("terminal session helper required forced cleanup")
		}
	})
	data := make(chan []byte, 16)
	stopRead := make(chan struct{})
	t.Cleanup(func() { close(stopRead) })
	go func() {
		for {
			b := make([]byte, 4096)
			n, e := master.Read(b)
			if n > 0 {
				select {
				case data <- b[:n]:
				case <-stopRead:
					return
				}
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
	write := func(b []byte) {
		t.Helper()
		if n, err := master.Write(b); err != nil || n != len(b) {
			t.Fatalf("PTY write: n=%d err=%v", n, err)
		}
	}
	write([]byte{3})
	expect("INTERRUPTED")
	size := [4]uint16{37, 101, 0, 0}
	if err = ioctl(master.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	expect("RESIZED 37 101")
	write([]byte("exit\n"))
	expect("RESTORED")
	select {
	case <-done:
		if waitErr != nil {
			t.Fatal(waitErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal session helper did not exit")
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForegroundTerminalChild$")
	cmd.Env = append(os.Environ(), "DEVBOX_TTY_TEST_HELPER=2")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	restore := foreground(cmd)
	err := cmd.Run()
	restore()
	if err != nil {
		t.Fatal(err)
	}
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

func TestForegroundTerminalChild(t *testing.T) {
	if os.Getenv("DEVBOX_TTY_TEST_HELPER") != "2" {
		return
	}
	// A shell can defer an INT trap until its next read completes even after
	// printing READY. Register Go's signal handler before publishing readiness
	// so an actual terminal Ctrl-C is observable without sending another line.
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGWINCH)
	defer signal.Stop(signals)
	var group int32
	if ioctl(os.Stdin.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&group)) != nil || int(group) != syscall.Getpgrp() {
		t.Fatal("child did not own terminal foreground")
	}
	var term syscall.Termios
	if err := ioctl(os.Stdin.Fd(), syscall.TCGETS, unsafe.Pointer(&term)); err != nil {
		t.Fatal(err)
	}
	if term.Lflag&syscall.ECHO == 0 {
		t.Fatal("fixture needs echo enabled before changing terminal mode")
	}
	term.Lflag &^= syscall.ECHO
	if err := ioctl(os.Stdin.Fd(), syscall.TCSETS, unsafe.Pointer(&term)); err != nil {
		t.Fatal(err)
	}
	input := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			input <- scanner.Text()
		}
		close(input)
	}()
	fmt.Fprintln(os.Stdout, "READY")
	for {
		select {
		case sig := <-signals:
			switch sig {
			case syscall.SIGINT:
				fmt.Fprintln(os.Stdout, "INTERRUPTED")
			case syscall.SIGWINCH:
				var size [4]uint16
				if err := ioctl(os.Stdin.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)); err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(os.Stdout, "RESIZED %d %d\n", size[0], size[1])
			}
		case action, ok := <-input:
			if !ok || action != "exit" {
				t.Fatalf("unexpected terminal input: %q (open=%t)", action, ok)
			}
			return
		}
	}
}
