//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/JosephWest2/cloud_dev/internal/execution"
)

const signalTestID = "dc1-0123456789abcdef0123456789abcdef"
const signalTestSSMID = "01234567-89ab-cdef-0123-456789abcdef"

// Only the test binary substitutes this controlled worker. The parent runs the
// production supervisor, including its executable boundary and process groups.
func TestMain(m *testing.M) {
	if mode := os.Getenv("DEVBOX_SUPERVISOR_TEST"); mode != "" && len(os.Args) > 1 && os.Args[1] == "__devbox_worker" {
		signals := make(chan os.Signal, 8)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		r := execution.Result{SchemaVersion: 1, Command: "exec", CommandID: signalTestID, SSMCommandID: signalTestSSMID, SubmissionState: "submitted", RecoveryCommand: "devbox logs " + signalTestID}
		fmt.Fprintf(os.Stderr, "submitted command_id=%s ssm_command_id=%s\n", signalTestID, signalTestSSMID)
		if strings.HasPrefix(mode, "completed-") {
			code, err := strconv.Atoi(strings.TrimPrefix(mode, "completed-"))
			if err != nil {
				os.Exit(99)
			}
			r.Outcome, r.ExitCode, r.OK = "remote_exit", code, code == 0
			_ = json.NewEncoder(os.Stdout).Encode(r)
			fmt.Fprintln(os.Stderr, "ESTABLISHED")
			// Establish completion first, then ensure the parent's context is
			// canceled before the child exits with its already recorded code.
			<-signals
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "OBSERVING")
		<-signals
		r.Outcome, r.ExitCode, r.Message = "interrupted", 4, "observation stopped; the remote command may continue"
		_ = json.NewEncoder(os.Stdout).Encode(r)
		os.Exit(4)
	}
	os.Exit(m.Run())
}

func TestSupervisorProcessHelper(t *testing.T) {
	if os.Getenv("DEVBOX_SUPERVISOR_TEST") == "" {
		return
	}
	os.Exit(supervise([]string{"exec"}))
}

func supervisorFixture(t *testing.T, mode string, terminal bool) (*exec.Cmd, io.ReadCloser, *bufio.Reader, *os.File) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorProcessHelper$")
	cmd.Env = append(os.Environ(), "DEVBOX_SUPERVISOR_TEST="+mode)
	var master, slave *os.File
	if terminal {
		var err error
		master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { master.Close() })
		var unlock int32
		var number uint32
		if ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)) != nil || ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&number)) != nil {
			t.Fatal("cannot initialize test PTY")
		}
		slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { slave.Close() })
		cmd.Stdin = slave
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	diag, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, out, bufio.NewReader(diag), master
}

func supervisorMarker(t *testing.T, diag *bufio.Reader, marker string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		for {
			line, err := diag.ReadString('\n')
			if err != nil {
				done <- err
				return
			}
			if strings.TrimSpace(line) == marker {
				done <- nil
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("supervisor stalled before %s", marker)
	}
}

func supervisorResult(t *testing.T, cmd *exec.Cmd, out io.Reader, wantCode int, wantOutcome string) {
	t.Helper()
	done := make(chan struct{})
	var data []byte
	var readErr, waitErr error
	go func() {
		data, readErr = io.ReadAll(io.LimitReader(out, 16*1024))
		waitErr = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not complete")
	}
	if readErr != nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != wantCode {
		t.Fatalf("supervisor exit contradicts worker result: want=%d wait=%v read=%v body=%s", wantCode, waitErr, readErr, data)
	}
	var r execution.Result
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&r) != nil || decoder.Decode(new(any)) != io.EOF || r.SchemaVersion != 1 || r.Command != "exec" || r.Outcome != wantOutcome || r.ExitCode != wantCode || r.CommandID != signalTestID || r.SSMCommandID != signalTestSSMID || r.RecoveryCommand == "" {
		t.Fatalf("invalid or repeated worker envelope: %s", data)
	}
}

func TestSupervisorPreservesEstablishedCompletionDuringInterrupt(t *testing.T) {
	for _, code := range []int{0, 1, 2, 4, 255} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cmd, out, diag, _ := supervisorFixture(t, "completed-"+strconv.Itoa(code), false)
			supervisorMarker(t, diag, "ESTABLISHED")
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			supervisorResult(t, cmd, out, code, "remote_exit")
		})
	}
}

func TestSupervisorDetachPreservesOneEnvelopeAndRecoveryIDs(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal bool
		signal   os.Signal
		repeated bool
	}{{"sigint", false, os.Interrupt, false}, {"sigterm", false, syscall.SIGTERM, false}, {"repeated_sigint", false, os.Interrupt, true}, {"terminal_ctrl_c", true, nil, false}} {
		t.Run(test.name, func(t *testing.T) {
			cmd, out, diag, terminal := supervisorFixture(t, "observing", test.terminal)
			supervisorMarker(t, diag, "OBSERVING")
			var err error
			if terminal != nil {
				_, err = terminal.Write([]byte{3})
			} else {
				err = cmd.Process.Signal(test.signal)
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.repeated {
				_ = cmd.Process.Signal(test.signal)
				_ = cmd.Process.Signal(test.signal)
			}
			supervisorResult(t, cmd, out, 4, "interrupted")
		})
	}
}
