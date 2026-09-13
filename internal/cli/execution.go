package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/JosephWest2/cloud_dev/internal/execution"
)

func emitExecution(r execution.Result, jsonMode bool, stdout, stderr io.Writer) int {
	if jsonMode {
		if err := json.NewEncoder(stdout).Encode(r); err != nil {
			fmt.Fprintln(stderr, "devbox: cannot write execution result; retain the earlier command ID")
			return 1
		}
	} else {
		if _, err := fmt.Fprintf(stdout, "%s", r.Outcome); err != nil {
			return 1
		}
		if r.CommandID != "" {
			if _, err := fmt.Fprintf(stdout, " command_id=%s", r.CommandID); err != nil {
				return 1
			}
		}
		if r.Workload != nil {
			if r.Workload.ExitCode != nil {
				if _, err := fmt.Fprintf(stdout, " remote_exit_code=%d", *r.Workload.ExitCode); err != nil {
					return 1
				}
			}
			if r.Workload.Signal != nil {
				if _, err := fmt.Fprintf(stdout, " remote_signal=%d", *r.Workload.Signal); err != nil {
					return 1
				}
			}
		}
		if r.Publication != "" {
			if _, err := fmt.Fprintf(stdout, " publication=%s", r.Publication); err != nil {
				return 1
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return 1
		}
	}
	if !r.OK {
		fmt.Fprintf(stderr, "devbox: %s: %s\n", r.Outcome, r.Message)
		if r.RecoveryCommand != "" {
			fmt.Fprintf(stderr, "devbox: recover with %s\n", r.RecoveryCommand)
		}
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(stderr, "devbox: %s; retain command_id=%s for recovery\n", warning, r.CommandID)
	}
	return r.ExitCode
}
