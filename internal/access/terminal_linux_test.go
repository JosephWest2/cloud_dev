//go:build linux

package access

import (
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

func ttyIOCTL(fd uintptr, request uintptr, pointer unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(pointer))
	if e != 0 {
		return e
	}
	return nil
}

// Use a controlling terminal and real OpenSSH multiplexing. Piped input cannot
// expose SIGTTIN when the master reads the terminal fd passed by its client.
func TestLocalSSHKeyboardInterruptResizeAndRestore(t *testing.T) {
	for _, mode := range []string{"exit", "cancel"} {
		t.Run(mode, func(t *testing.T) { testLocalSSHTerminal(t, mode) })
	}
}

func testLocalSSHTerminal(t *testing.T, mode string) {
	cfg := localSSHConfig(t)
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	var unlock int32
	if err = ttyIOCTL(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Fatal(err)
	}
	var number uint32
	if err = ttyIOCTL(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLocalSSHTerminalHelper$")
	cmd.Env = append(os.Environ(), "DEVBOX_SSH_TTY_CONFIG="+cfg, "DEVBOX_SSH_TTY_MODE="+mode)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		if waited {
			return
		}
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()
	data := make(chan []byte, 64)
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
	expect := func(wants ...string) {
		s := strings.Join(wants, " or ")
		matched := func() bool {
			for _, want := range wants {
				if strings.Contains(collected.String(), want) {
					return true
				}
			}
			return false
		}
		t.Helper()
		timer := time.NewTimer(12 * time.Second)
		defer timer.Stop()
		for !matched() {
			select {
			case b, ok := <-data:
				if !ok {
					t.Fatalf("PTY ended before %q: %s", s, &collected)
				}
				collected.Write(b)
			case <-timer.C:
				t.Fatalf("SSH keyboard stalled before %q: %s", s, &collected)
			}
		}
	}
	// Wait for the remote prompt before sending input; echoed input is not proof
	// the remote command ran. Markers are built from separate printf arguments.
	expect("$ ", "# ")
	// Both direct SSH children (master and mux) must share the foreground group.
	listing, err := exec.Command("ps", "--ppid", strconv.Itoa(cmd.Process.Pid), "-o", "pgid=,tpgid=,comm=").Output()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(listing), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[2] == "ssh" {
			count++
			if fields[0] != fields[1] {
				t.Fatalf("SSH child outside foreground group: %s", line)
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected master and mux: %s", listing)
	}

	master.Write([]byte("printf 'KEYBOARD_%s\\n' OK\n"))
	expect("KEYBOARD_OK")
	master.Write([]byte("sleep 30\n"))
	time.Sleep(200 * time.Millisecond)
	master.Write([]byte{3})
	master.Write([]byte("printf 'INTERRUPT_%s\\n' OK\n"))
	expect("INTERRUPT_OK")
	size := [4]uint16{37, 101, 0, 0}
	if err = ttyIOCTL(master.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	master.Write([]byte("stty size\n"))
	expect("37 101")
	if mode == "cancel" {
		cmd.Process.Signal(syscall.SIGTERM)
	} else {
		master.Write([]byte("exit 4\n"))
	}
	expect("TERMINAL_RESTORED")
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatalf("terminal helper: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal helper did not exit")
	}
}

func TestLocalSSHTerminalHelper(t *testing.T) {
	cfg := os.Getenv("DEVBOX_SSH_TTY_CONFIG")
	if cfg == "" {
		return
	}
	var before syscall.Termios
	if err := ttyIOCTL(os.Stdin.Fd(), syscall.TCGETS, unsafe.Pointer(&before)); err != nil {
		t.Fatal(err)
	}
	signals, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signals, 30*time.Second)
	defer cancel()
	setup, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	code, err := interactive(ctx, setup, Artifacts{Alias: "local-test", ConfigPath: cfg}, os.Stdin, os.Stdout, os.Stderr)
	want := 4
	if os.Getenv("DEVBOX_SSH_TTY_MODE") == "cancel" {
		want = 255
	}
	if code != want || err != nil {
		t.Fatalf("SSH exit: %d %v", code, err)
	}
	var after syscall.Termios
	var group int32
	if ttyIOCTL(os.Stdin.Fd(), syscall.TCGETS, unsafe.Pointer(&after)) != nil || before != after {
		t.Fatal("terminal mode not restored")
	}
	if ttyIOCTL(os.Stdin.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&group)) != nil || int(group) != syscall.Getpgrp() {
		t.Fatal("terminal owner not restored")
	}
	fmt.Println("TERMINAL_RESTORED")
}
