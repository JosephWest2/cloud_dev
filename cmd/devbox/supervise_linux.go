package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// The SDK's credential cache deliberately lets retrieval outlive a caller's
// context. Run the CLI in a process group so provider processes (and their
// ordinary shell descendants) cannot outlive the command, even on timeout.
// This preserves the SDK credential chain without reimplementing its providers.
func supervise(args []string) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot locate devbox executable")
		return 1
	}
	// main exits after this one supervised command. Keep handling signals until
	// process exit so a repeated interrupt cannot replace an established status.
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	cmd := exec.CommandContext(ctx, executable, append([]string{"__devbox_worker"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	restore := foreground(cmd)
	defer restore()
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// Let the worker emit its structured cancellation result before cleanup.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// Allow the access proxy's independent 5s SSM cleanup before force-kill.
	cmd.WaitDelay = 7 * time.Second
	if err = cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot start devbox worker")
		return 1
	}
	// The worker may exit while a credential helper is still retrieving. Kill
	// the entire group on every completion path, including normal failure.
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	err = cmd.Wait()
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		// CommandContext can return context.Canceled even after the worker
		// established completion and exited 0. Preserve the worker's actual
		// status so a racing local signal cannot contradict its final JSON.
		return cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return 4
	}
	fmt.Fprintln(os.Stderr, "devbox worker failed")
	return 1
}
