package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
)

func emitBatch(r lifecycle.BatchResult, prefix string, jsonMode bool, stdout, stderr io.Writer) int {
	stdout = completeOutput{stdout}
	if prefix == "" {
		prefix = "devbox"
	}
	// Print recovery evidence even when the final stdout write fails, including
	// failures following a completely successful allocation/readiness result.
	recoverIDs := func() {
		for _, worker := range r.Workers {
			fmt.Fprintf(stderr, "devbox: retained instance %s; inspect: %s ls --json; cleanup: %s down %s --timeout 5m\n", worker.ID, prefix, prefix, worker.ID)
			for _, volume := range worker.Volumes {
				fmt.Fprintf(stderr, "devbox: retained volume %s instance=%s root=%t deletion=%s\n", volume.ID, worker.ID, volume.Root, volume.Deletion)
			}
		}
		if r.ResumeCommand != "" {
			fmt.Fprintf(stderr, "devbox: recover with: %s\n", r.ResumeCommand)
		}
		if r.RetryCommand != "" {
			fmt.Fprintf(stderr, "devbox: explicit missing-capacity retry: %s\n", r.RetryCommand)
		}
	}
	var writeErr error
	if jsonMode {
		writeErr = json.NewEncoder(stdout).Encode(r)
	} else {
		writeErr = writeBatchText(stdout, r)
	}
	if writeErr != nil {
		fmt.Fprintln(stderr, "devbox: cannot write command result; preserve the recovery identities below")
		recoverIDs()
		return 1
	}
	if !r.OK {
		fmt.Fprintf(stderr, "devbox: %s: %s\n", r.Code, r.Message)
		recoverIDs()
	}
	return r.ExitCode
}

func writeBatchText(w io.Writer, r lifecycle.BatchResult) error {
	missing := "unknown"
	if r.MissingCount != nil {
		missing = fmt.Sprint(*r.MissingCount)
	}
	if _, err := fmt.Fprintf(w, "%s: %s\nrequest=%s requested=%d fulfilled=%d ready=%d missing=%s\n", r.Code, r.Message, r.RequestID, r.RequestedCount, r.FulfilledCount, r.ReadyCount, missing); err != nil {
		return err
	}
	if r.Plan.SchemaVersion == 1 {
		if _, err := fmt.Fprintf(w, "profile=%s region=%s market=%s base=%q group=%q template=%s/%s image=%s\n", r.Plan.Profile, r.Plan.Region, r.Plan.Market, r.Plan.BaseName, r.Plan.Group, r.Plan.Image.LaunchTemplateID, r.Plan.Image.LaunchTemplateVersion, r.Plan.Image.AMIID); err != nil {
			return err
		}
	}
	for _, attempt := range r.Attempts {
		if _, err := fmt.Fprintf(w, "attempt=%s parent=%s fleet=%s status=%s requested=%d fulfilled=%d\n", attempt.AttemptID, attempt.ParentID, attempt.FleetID, attempt.Status, attempt.RequestedCount, attempt.FulfilledCount); err != nil {
			return err
		}
	}
	for _, worker := range r.Workers {
		if _, err := fmt.Fprintf(w, "%s name=%q group=%q attempt=%s type=%s subnet=%s az=%s market=%s ec2=%s ssm=%s bootstrap=%s readiness=%s status=%s\n", worker.ID, worker.Name, worker.Group, worker.AttemptID, worker.Type, worker.SubnetID, worker.AvailabilityZone, worker.Market, worker.State, worker.SSM, worker.Bootstrap, worker.Readiness, worker.Status); err != nil {
			return err
		}
		if worker.ObservationCode != "" || worker.ProbeCommandID != "" {
			if _, err := fmt.Fprintf(w, "  observation=%s probe_command=%s\n", worker.ObservationCode, worker.ProbeCommandID); err != nil {
				return err
			}
		}
		for _, volume := range worker.Volumes {
			if _, err := fmt.Fprintf(w, "  volume=%s root=%t delete_on_termination=%t deletion=%s\n", volume.ID, volume.Root, volume.DeleteOnTermination, volume.Deletion); err != nil {
				return err
			}
		}
	}
	for _, diagnostic := range r.Errors {
		if _, err := fmt.Fprintf(w, "error=%s resource=%s type=%s subnet=%s: %s\n", diagnostic.Code, diagnostic.ResourceID, diagnostic.InstanceType, diagnostic.SubnetID, diagnostic.Message); err != nil {
			return err
		}
	}
	return nil
}
