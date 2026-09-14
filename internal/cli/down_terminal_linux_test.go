//go:build linux

package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestDownTerminalDetectionAndPTYConfirmation(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	var unlock int32
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != 0 {
		t.Fatal(err)
	}
	var number uint32
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); err != 0 {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	pipe, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pipe.Close() })
	if _, err := writer.WriteString("yes\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	for _, tc := range []struct {
		name, mode string
		input      *os.File
	}{
		{name: "pty detection", mode: "terminal", input: slave},
		{name: "pipe rejects yes", mode: "pipe", input: pipe},
		{name: "pty confirmation", mode: "approve", input: slave},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mode == "approve" {
				if _, err := master.WriteString("yes\n"); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDownTerminalHelper$")
			cmd.Env = append(os.Environ(), "DEVBOX_DOWN_TERMINAL_TEST="+tc.mode)
			cmd.Stdin = tc.input
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("terminal helper: %v\n%s", err, output)
			}
		})
	}
}

func TestDownTerminalHelper(t *testing.T) {
	mode := os.Getenv("DEVBOX_DOWN_TERMINAL_TEST")
	if mode == "" {
		return
	}
	wantTerminal := mode != "pipe"
	if got := downInputIsTerminal(); got != wantTerminal {
		t.Fatalf("terminal=%t, want %t", got, wantTerminal)
	}
	if mode == "terminal" {
		return
	}
	scope, candidates := downConfirmationFixture()
	var output bytes.Buffer
	approved, err := newDownConfirmation(os.Stdin, &output, downInputIsTerminal)(context.Background(), scope, candidates, false)
	assertDownConfirmationPreview(t, output.String())
	if mode == "approve" {
		if !approved || err != nil {
			t.Fatalf("PTY affirmative failed: approved=%t err=%v", approved, err)
		}
	} else {
		if approved {
			t.Fatal("piped input approved")
		}
		assertDownConfirmationError(t, err, "confirmation_required")
	}
}
