package expirycleanup

import (
	"context"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func setRootDiagnostic(o *expiry.Outcome, code string) {
	o.RootDeletion = "unavailable"
	if code == "root_volume_retained" {
		o.RootDeletion = "retained"
	}
}
func (s *Service) prepare(ctx context.Context, runID string, w *worker) {
	o := &w.outcome
	historical := historicalMappingsAbsent(w.record, w.selected.Reason)
	code := rootProblem(w.record)
	if code != "" && !historical {
		addProblem(o, code)
		setRootDiagnostic(o, code)
		if w.selected.Eligible {
			return
		}
	}
	if w.selected.Eligible {
		// Acknowledgement precedes the final read. A slow sink cannot enlarge the
		// read-to-dispatch interval or let a stale clock authorize termination.
		if err := s.emit(ctx, runID, "termination_prepared", o, nil); err != nil {
			addProblem(o, "evidence_unavailable")
			return
		}
	}
	current, ok := s.exact(ctx, w.selected.InstanceID)
	if !ok {
		addProblem(o, "resource_unverified")
		return
	}
	now := s.deps.Clock.Now()
	d := expiry.Evaluate(s.scope, current.resource, now)
	if w.selected.Eligible {
		d = expiry.Recheck(s.scope, w.selected, current.resource, now)
	} else if d.ExpiresAt != nil && w.selected.ExpiresAt != nil && *d.ExpiresAt != *w.selected.ExpiresAt {
		d.Eligible = false
		d.Reason = expiry.ExpiryChanged
	}
	// Preserve the original selection and its timestamp in output; the current
	// recheck reason/state explains why a frozen candidate was skipped.
	o.State = d.State
	if !d.Eligible && d.Reason != expiry.AlreadyTerminated && d.Reason != expiry.AlreadyTerminating {
		o.Status = "skipped"
		o.Eligible = false
		o.Reason = d.Reason
		if !benign(d.Reason) {
			addProblem(o, string(d.Reason))
		}
		return
	}
	if d.Reason == expiry.AlreadyTerminated {
		o.Status = "already_terminated"
		w.terminal = true
	}
	if !sameMappings(w.record, current) {
		// A terminal instance may have lost all mappings. Only mappings captured in
		// this invocation remain authority; a different nonempty mapping is drift.
		if d.Reason != expiry.AlreadyTerminated || len(current.volumes) > 0 || current.badMapping {
			o.Volumes = mergeMappings(o.Volumes, current.volumes)
			addProblem(o, "root_volume_unverified")
			o.RootDeletion = "unavailable"
			return
		}
	}
	if d.Reason == expiry.AlreadyTerminated {
		o.Status = "already_terminated"
		w.terminal = true
		if historical {
			// The exact recheck established terminal EC2 state, but this run
			// captured no volume authority. Keep deletion unavailable without
			// turning ordinary historical absence into a cleanup failure.
			if !historicalMappingsAbsent(current, d.Reason) {
				addProblem(o, "root_volume_unverified")
			}
			return
		}
		w.active = true
		return
	}
	if d.Reason == expiry.AlreadyTerminating {
		o.Status = "already_terminating"
		w.active = true
		return
	}
	// Terminal observations selected by discovery cannot turn into candidates.
	if !w.selected.Eligible {
		addProblem(o, "resource_unverified")
		return
	}
	if ctx.Err() != nil {
		addProblem(o, "interrupted")
		return
	}
	request, cancel := context.WithTimeout(ctx, s.limits.RequestTimeout)
	out, err := s.deps.EC2.TerminateInstances(request, &ec2.TerminateInstancesInput{InstanceIds: []string{w.selected.InstanceID}}, s.mutation)
	requestErr := request.Err()
	cancel()
	o.Status = "termination_unknown"
	switch {
	case apiCode(err, "UnauthorizedOperation", "AuthFailure", "AccessDenied", "AccessDeniedException"):
		o.Status = "termination_denied"
		addProblem(o, "termination_denied")
		return
	case apiCode(err, "OperationNotPermitted", "Client.OperationNotPermitted"):
		o.Status = "termination_denied"
		addProblem(o, "termination_protected")
		return
	}
	if err == nil && requestErr == nil && exactAcknowledgement(out, w.selected.InstanceID) {
		o.Status = "termination_requested"
	}
	// A lost/malformed acknowledgement may have followed an accepted request.
	// Observation can resolve it, but this run never dispatches this ID again.
	w.active = true
}
func exactAcknowledgement(out *ec2.TerminateInstancesOutput, id string) bool {
	if out == nil || len(out.TerminatingInstances) != 1 {
		return false
	}
	change := out.TerminatingInstances[0]
	if aws.ToString(change.InstanceId) != id || change.CurrentState == nil || change.PreviousState == nil {
		return false
	}
	switch change.PreviousState.Name {
	case "pending", "running", "stopping", "stopped", "shutting-down", "terminated":
	default:
		return false
	}
	return change.CurrentState.Name == "shutting-down" || change.CurrentState.Name == "terminated"
}
func (s *Service) observe(ctx context.Context, w *worker) {
	if ctx.Err() != nil {
		return
	}
	o := &w.outcome
	if !w.terminal {
		current, ok := s.exact(ctx, w.selected.InstanceID)
		if !ok {
			return
		} // Missing is not terminal evidence; later rounds can recover.
		d := expiry.Evaluate(s.scope, current.resource, s.deps.Clock.Now())
		if d.Reason != expiry.AlreadyTerminated {
			return
		}
		if d.ExpiresAt == nil || w.selected.ExpiresAt == nil || *d.ExpiresAt != *w.selected.ExpiresAt {
			return
		}
		if !sameMappings(w.record, current) && (len(current.volumes) > 0 || current.badMapping) {
			o.Volumes = mergeMappings(o.Volumes, current.volumes)
			addProblem(o, "root_volume_unverified")
			o.RootDeletion = "unavailable"
			o.State = "terminated"
			o.Status = "termination_observed"
			w.terminal = true
			w.active = false
			return
		}
		o.State = "terminated"
		o.Status = "termination_observed"
		w.terminal = true
	}
	if code := rootProblem(w.record); code != "" {
		setRootDiagnostic(o, code)
		// The initial diagnostic is already present; root deletion cannot be
		// established even though exact EC2 termination was observed.
		if !hasProblem(*o, code) {
			addProblem(o, code)
		}
	}
	// Rotate through exact captured IDs, at most one volume request per turn.
	// A slow non-root volume cannot consume every turn before its peers.
	for step := 0; step < len(o.Volumes); step++ {
		i := (w.nextVolume + step) % len(o.Volumes)
		v := &o.Volumes[i]
		if v.Deletion == "deleted" || v.Deletion == "retained" {
			continue
		}
		if v.DeleteOnTermination == nil {
			v.Deletion = "unavailable"
			if !hasResourceProblem(*o, v.ID, "volume_unresolved") {
				o.Errors = append(o.Errors, problem(v.ID, "volume_unresolved"))
			}
			continue
		}
		if !*v.DeleteOnTermination {
			v.Deletion = "retained"
			continue
		}
		// A bad/contradictory mapping never authorizes deletion evidence.
		if w.record.badMapping {
			v.Deletion = "unavailable"
			continue
		}
		out, err := s.describeVolumes(ctx, v.ID)
		deleted := apiCode(err, "InvalidVolume.NotFound") && out == nil
		if err == nil && s.exactVolume(out, v.ID) && out.Volumes[0].State == "deleted" {
			deleted = true
		}
		if deleted {
			v.Deletion = "deleted"
		}
		w.nextVolume = (i + 1) % len(o.Volumes)
		break
	}
	pending := false
	for _, v := range o.Volumes {
		if v.DeleteOnTermination != nil && *v.DeleteOnTermination && !w.record.badMapping && v.Deletion != "deleted" {
			pending = true
		}
	}
	if rootProblem(w.record) == "" {
		for _, v := range o.Volumes {
			if v.Root {
				o.RootDeletion = v.Deletion
			}
		}
	}
	w.active = pending
	if !pending {
		unresolvedVolumes(o)
	}
}
func hasProblem(o expiry.Outcome, code string) bool {
	for _, p := range o.Errors {
		if p.Code == code {
			return true
		}
	}
	return false
}
func hasResourceProblem(o expiry.Outcome, id, code string) bool {
	for _, p := range o.Errors {
		if p.ResourceID == id && p.Code == code {
			return true
		}
	}
	return false
}
func unresolvedVolumes(o *expiry.Outcome) {
	for i := range o.Volumes {
		v := &o.Volumes[i]
		if v.Deletion != "deleted" && v.Deletion != "retained" {
			v.Deletion = "unavailable"
			if !hasResourceProblem(*o, v.ID, "volume_unresolved") {
				o.Errors = append(o.Errors, problem(v.ID, "volume_unresolved"))
			}
			if v.Root {
				o.RootDeletion = "unavailable"
			}
		}
	}
}
