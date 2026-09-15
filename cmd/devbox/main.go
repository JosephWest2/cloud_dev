package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/JosephWest2/cloud_dev/internal/cli"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
)

func main() {
	args := os.Args[1:]
	worker := len(args) > 0 && args[0] == "__devbox_worker"
	if worker {
		args = args[1:]
	}
	if cli.IsCleanupCommand(args) {
		// Go normally exits on SIGPIPE when inherited stdout/stderr is closed.
		// Cleanup must instead receive EPIPE so a failed evidence write denies
		// dispatch and a failed result write preserves the fallback envelope.
		// Install this in both supervisor and worker through their one-command
		// process lifetime; other commands retain their existing pipe behavior.
		signal.Ignore(syscall.SIGPIPE)
	}
	if worker {
		os.Exit(runCLI(args))
	}
	os.Exit(supervise(args))
}

func runCLI(args []string) int {
	// Keep SDK credential_process stderr outside the public diagnostic stream.
	// The SDK explicitly gives helpers os.Stderr (including arbitrary provider
	// errors and MFA prompts). devbox requires authentication before invocation.
	// Set this once, before any goroutines; retain the real writer for our own
	// allowlisted diagnostics. Do not mutate process streams inside library code.
	diagnostics := os.Stderr
	quiet, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return 1
	}
	defer quiet.Close()
	os.Stderr = quiet
	// This worker handles exactly one command before main calls os.Exit. Keep
	// signal handling installed through that exit: resetting it after writing
	// final JSON would let a repeated signal contradict the established result.
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return cli.Run(ctx, args, os.Stdout, diagnostics, doctor.DefaultDependencies())
}
