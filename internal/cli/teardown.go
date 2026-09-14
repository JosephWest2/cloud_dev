package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
)

func emitTeardown(r lifecycle.TeardownResult, prefix string, jsonMode bool, stdout, stderr io.Writer) int {
	stdout = completeOutput{stdout}
	if prefix == "" {
		prefix = "devbox"
	}
	var err error
	if jsonMode {
		err = json.NewEncoder(stdout).Encode(r)
	} else {
		err = writeTeardownText(stdout, r)
	}
	if err != nil {
		fmt.Fprintln(stderr, "devbox: cannot write teardown result; retain the cleanup evidence below")
	}
	if !r.OK || err != nil {
		fmt.Fprintf(stderr, "devbox: %s: %s\n", r.Code, r.Message)
		for _, worker := range r.Workers {
			fmt.Fprintf(stderr, "devbox: instance %s termination=%s root_deletion=%s; inspect: %s ls --json; retry cleanup: %s down %s --timeout 5m\n", worker.ID, worker.Status, worker.RootDeletion, prefix, prefix, worker.ID)
			for _, volume := range worker.Volumes {
				fmt.Fprintf(stderr, "devbox: volume %s instance=%s root=%t deletion=%s\n", volume.ID, worker.ID, volume.Root, volume.Deletion)
			}
		}
	}
	if err != nil {
		return 1
	}
	return r.ExitCode
}

func writeTeardownText(w io.Writer, r lifecycle.TeardownResult) error {
	if _, err := fmt.Fprintf(w, "%s: %s\nselected=%d terminated=%d cleaned=%d\n", r.Code, r.Message, r.SelectedCount, r.TerminatedCount, r.CleanedCount); err != nil {
		return err
	}
	for _, worker := range r.Workers {
		if _, err := fmt.Fprintf(w, "%s name=%q group=%q ec2=%s termination=%s root_deletion=%s\n", worker.ID, worker.Name, worker.Group, worker.State, worker.Status, worker.RootDeletion); err != nil {
			return err
		}
		for _, volume := range worker.Volumes {
			if _, err := fmt.Fprintf(w, "  volume=%s root=%t delete_on_termination=%t deletion=%s\n", volume.ID, volume.Root, volume.DeleteOnTermination, volume.Deletion); err != nil {
				return err
			}
		}
		for _, diagnostic := range worker.Errors {
			if _, err := fmt.Fprintf(w, "  error=%s: %s\n", diagnostic.Code, diagnostic.Message); err != nil {
				return err
			}
		}
	}
	for _, diagnostic := range r.Errors {
		if _, err := fmt.Fprintf(w, "error=%s resource=%s: %s\n", diagnostic.Code, diagnostic.ResourceID, diagnostic.Message); err != nil {
			return err
		}
	}
	return nil
}
