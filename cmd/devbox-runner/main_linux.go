package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/runner"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == runner.WorkloadMode {
		os.Exit(runner.WorkloadMain())
	}
	os.Exit(run())
}
func run() int {
	started := time.Now()
	if len(os.Args) != 3 || os.Args[1] != "--config" {
		fmt.Fprintln(os.Stderr, "runner_usage_invalid")
		return 1
	}
	syscall.Umask(022)
	c, err := runner.LoadConfig(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner_config_invalid")
		return 1
	}
	in := runner.Input{CommandID: os.Getenv("SSM_requestId"), SSMCommandID: os.Getenv("SSM_COMMAND_ID"), EncodedPayload: os.Getenv("SSM_payload")}
	p, err := execprotocol.DecodePayload(in.EncodedPayload)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner_payload_invalid")
		return 1
	}
	// Last resort independent of cooperative cancellation or a blocked filesystem.
	watchdog := time.AfterFunc(time.Duration(p.ExecTimeoutSeconds)*time.Second+180*time.Second, func() { os.Exit(1) })
	defer watchdog.Stop()
	d, err := runner.ProductionDependencies(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner_setup_failed")
		return 1
	}
	d.PreparationDeadline = started.Add(30 * time.Second)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := runner.Run(ctx, c, in, d)
	fmt.Fprintln(os.Stderr, r.Code)
	return r.ExitCode
}
