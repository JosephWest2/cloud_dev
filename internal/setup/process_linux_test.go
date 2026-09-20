//go:build linux

package setup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func ioctlTest(fd uintptr, request uint, ptr unsafe.Pointer) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request), uintptr(ptr))
	if err != 0 {
		return err
	}
	return nil
}

func TestSetupPhysicalCtrlCDrainsToolExactlyOnce(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	var unlock int32
	if err = ioctlTest(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Fatal(err)
	}
	var number uint32
	if err = ioctlTest(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	result := filepath.Join(t.TempDir(), "signal-result")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupProcessHelper$")
	cmd.Env = append(os.Environ(), "DEVBOX_SETUP_PROCESS_TEST=parent", "DEVBOX_SETUP_SIGNAL_RESULT="+result)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	chunks := make(chan string, 10)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	text := ""
	deadline := time.After(5 * time.Second)
	for !strings.Contains(text, "READY") {
		select {
		case chunk := <-chunks:
			text += chunk
		case <-deadline:
			t.Fatal("tool did not become ready", text)
		}
	}
	if _, err = master.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err = master.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful drain did not complete")
	}
	b, err := os.ReadFile(result)
	if err != nil || string(b) != "one interrupt; persisted" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestSetupProcessHelper(t *testing.T) {
	mode := os.Getenv("DEVBOX_SETUP_PROCESS_TEST")
	if mode == "" {
		return
	}
	if mode == "tool" {
		signals := make(chan os.Signal, 8)
		signal.Notify(signals, os.Interrupt)
		fmt.Fprintln(os.Stderr, "READY")
		<-signals
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-signals:
			_ = os.WriteFile(os.Getenv("DEVBOX_SETUP_SIGNAL_RESULT"), []byte("DUPLICATE"), 0600)
			os.Exit(1)
		case <-timer.C:
		}
		_ = os.WriteFile(os.Getenv("DEVBOX_SETUP_SIGNAL_RESULT"), []byte("one interrupt; persisted"), 0600)
		os.Exit(0)
	}
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
	run := DefaultProcess(os.Stdin, os.Stderr)
	// The noninteractive tool writes readiness through a separate inherited file,
	// since ordinary tool diagnostics are intentionally discarded.
	script := filepath.Join(filepath.Dir(os.Getenv("DEVBOX_SETUP_SIGNAL_RESULT")), "tool.sh")
	body := "#!/bin/sh\nexec \"$DEVBOX_SETUP_TEST_BINARY\" -test.run=^TestSetupProcessHelper$ 2>\"$DEVBOX_SETUP_TEST_TTY\"\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		os.Exit(2)
	}
	tty, err := os.Readlink("/proc/self/fd/0")
	if err != nil {
		os.Exit(2)
	}
	env := append(os.Environ(), "DEVBOX_SETUP_PROCESS_TEST=tool", "DEVBOX_SETUP_TEST_BINARY="+os.Args[0], "DEVBOX_SETUP_TEST_TTY="+tty)
	_, _ = run(ctx, Request{Program: script, Env: env, Timeout: 5 * time.Second})
	os.Exit(0)
}

func TestMigrationPromptAdapterRefusesOverwriteAndEOF(t *testing.T) {
	for _, tc := range []struct {
		prompt   string
		approved bool
	}{
		{migrationPrompt + "\n  Pre-existing state was found while migrating the previous \"local\" backend to the\n  newly configured \"s3\" backend. No existing state was found in the newly\n  configured \"s3\" backend. Do you want to copy this state to the new \"s3\"\n  backend? Enter \"yes\" to copy and \"no\" to start with an empty state.\n\n  Enter a value: ", true},
		{"Do you want to overwrite existing remote state?\nEnter a value: ", false},
	} {
		var b bytes.Buffer
		c := &capture{migration: true, stdin: nopWriteCloser{&b}}
		// Prompt recognition must work across arbitrary pipe-read boundaries.
		for _, part := range []string{tc.prompt[:10], tc.prompt[10:]} {
			_, _ = c.Write([]byte(part))
		}
		if c.approved != tc.approved {
			t.Fatal("unexpected approval")
		}
		if tc.approved && b.String() != "yes\n" {
			t.Fatal(b.String())
		}
		if !tc.approved && b.Len() != 0 {
			t.Fatal("answered an unknown prompt", b.String())
		}
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
