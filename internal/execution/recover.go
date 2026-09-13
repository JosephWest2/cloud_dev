package execution

import (
	"context"
	"errors"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

type RecoverOptions struct {
	Now func() time.Time
}

type recovery struct {
	store   execprotocol.Store
	scope   execprotocol.Scope
	id      string
	now     func() time.Time
	result  Result
	binding *execprotocol.Binding
	records map[string]execprotocol.Record
	invalid *observationIssue
}

func (r *recovery) finish(outcome, code string, exit int, message string) Result {
	r.result.Outcome, r.result.Code, r.result.ExitCode, r.result.Message = outcome, code, exit, message
	r.result.OK = outcome == "complete" && exit == 0
	return r.result
}

func (r *recovery) stopped(err error) Result {
	if errors.Is(err, context.Canceled) {
		return r.finish("interrupted", "interrupted", 4, "local result retrieval stopped; recover again using the command ID")
	}
	return r.finish("timeout", "timeout", 4, "local result retrieval deadline expired; recover again using the command ID")
}

func (r *recovery) warn(code string) {
	for _, previous := range r.result.Warnings {
		if previous == code {
			return
		}
	}
	r.result.Warnings = append(r.result.Warnings, code)
}

func recoveryIssue(err error) *observationIssue {
	issue := storageObservationIssue(err)
	switch issue.outcome {
	case "result_corrupt":
		issue.outcome, issue.code = "corrupt", "corrupt"
	case "api_failed":
		issue.outcome = "unavailable"
		if errors.Is(err, execprotocol.ErrDenied) {
			issue.outcome, issue.code = "access_denied", "access_denied"
		}
	}
	return issue
}

func (r *recovery) fail(issue *observationIssue) Result {
	return r.finish(issue.outcome, issue.code, 1, issue.message)
}

// read retries only transient read failures, at most twice. Each attempt has an
// independent bound inside the caller's overall retrieval deadline. Absence is
// a snapshot, not a reason to poll for remote completion.
func (r *recovery) read(ctx context.Context, kind string) (execprotocol.Record, time.Time, error) {
	for attempt := 0; ; attempt++ {
		call, cancel := context.WithTimeout(ctx, observationCallTimeout)
		record, modified, err := execprotocol.ReadRecord(call, r.store, execprotocol.ObjectKey(r.scope, r.id, kind+".json"))
		cancel()
		if err == nil && (record.Scope != r.scope || record.CommandID != r.id || record.Kind != kind) {
			err = execprotocol.ErrCorrupt
		}
		if err == nil || errors.Is(err, execprotocol.ErrNotFound) || ctx.Err() != nil || attempt == 1 || !recoveryIssue(err).retryable {
			return record, modified, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return execprotocol.Record{}, time.Time{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// accept discovers identities from strictly decoded cloud records, rather than
// comparing historical commands against today's runtime manifest. Every record
// actually observed must agree; contradictions cannot be repaired by a later
// successful read. Already trusted workload evidence survives those failures.
func (r *recovery) accept(record execprotocol.Record, modified time.Time) bool {
	corrupt := func() bool {
		r.invalid = recoveryIssue(execprotocol.ErrCorrupt)
		return false
	}
	if r.binding != nil && record.Binding != *r.binding {
		return corrupt()
	}
	if r.result.SSMCommandID != "" && record.SSMCommandID != "" && r.result.SSMCommandID != record.SSMCommandID {
		return corrupt()
	}
	submitted, expires := record.SubmittedAt, record.ExpiresAt
	if record.Kind == "request" {
		// The request contains a retention duration, never a client timestamp.
		// Its authenticated S3 creation timestamp is the retention authority.
		if modified.IsZero() {
			return corrupt()
		}
		submitted = execprotocol.Timestamp(modified)
		if _, err := execprotocol.ParseTimestamp(submitted); err != nil {
			return corrupt()
		}
		expires = execprotocol.Timestamp(modified.Truncate(time.Second).Add(time.Duration(record.RetentionDays) * 24 * time.Hour))
		if _, err := execprotocol.ParseTimestamp(expires); err != nil {
			return corrupt()
		}
	}
	if submitted != "" && r.result.SubmittedAt != "" && (submitted != r.result.SubmittedAt || expires != r.result.ExpiresAt) {
		return corrupt()
	}
	if record.Kind == "started" {
		started, _ := execprotocol.ParseTimestamp(record.StartedAt)
		for _, existing := range r.records {
			if existing.FinishedAt != "" {
				finished, _ := execprotocol.ParseTimestamp(existing.FinishedAt)
				if finished.Before(started) {
					return corrupt()
				}
			}
		}
	}
	if record.FinishedAt != "" {
		if started, ok := r.records["started"]; ok {
			start, _ := execprotocol.ParseTimestamp(started.StartedAt)
			finish, _ := execprotocol.ParseTimestamp(record.FinishedAt)
			if finish.Before(start) {
				return corrupt()
			}
		}
		for _, kind := range []string{"outcome", "result"} {
			if existing, ok := r.records[kind]; ok && !sameOutcome(existing, record) {
				return corrupt()
			}
		}
	}
	if r.binding == nil {
		binding := record.Binding
		r.binding = &binding
		r.result.InstanceID = binding.InstanceID
		r.result.DurableState, r.result.SubmissionState = DurablePending, "submission_unknown"
	}
	if record.SSMCommandID != "" {
		r.result.SSMCommandID, r.result.SubmissionState = record.SSMCommandID, "submitted"
	}
	if submitted != "" {
		r.result.SubmittedAt, r.result.ExpiresAt = submitted, expires
	}
	switch record.Kind {
	case "started":
		r.result.StartedAt = record.StartedAt
		if r.result.Workload == nil {
			r.result.DurableState = DurableStarted
		}
	case "outcome", "result":
		r.result.Workload, r.result.Streams, r.result.Capture = record.Workload, record.Streams, record.Capture
		r.result.FinishedAt = record.FinishedAt
		r.result.DurableState, r.result.Publication = DurableOutcome, "pending"
		if record.Kind == "result" {
			r.result.DurableState, r.result.Publication = DurableFinal, record.Publication
		}
	}
	r.records[record.Kind] = record
	return true
}

func (r *recovery) expired() bool {
	if r.result.ExpiresAt == "" {
		return false
	}
	expires, _ := execprotocol.ParseTimestamp(r.result.ExpiresAt)
	return !r.now().Before(expires)
}

func (r *recovery) final(record execprotocol.Record) Result {
	if r.expired() {
		return r.finish("expired", "expired", 1, "the command result retention deadline has passed")
	}
	if record.Capture != "complete" || record.Publication != "complete" || record.Streams.Stdout.Upload != "complete" || record.Streams.Stderr.Upload != "complete" {
		return r.finish("incomplete", "incomplete", 1, "the workload outcome is recorded but capture or output publication is incomplete")
	}
	return r.finish("complete", "complete", 0, "complete output publication is recorded; stream bytes have not been downloaded or verified")
}

// Recover takes a bounded metadata snapshot using only a trusted storage scope
// and public command ID. A standalone final record is sufficient, including
// after worker removal, runtime upgrades and SSM history expiry. This function
// does not read output bytes, follow running commands, dispatch or cancel work.
// Retrieval success is independent of the recorded workload's exit or timeout.
func Recover(ctx context.Context, store execprotocol.Store, api InvocationAPI, scope execprotocol.Scope, commandID string, options RecoverOptions) Result {
	r := recovery{store: store, scope: scope, id: commandID, now: options.Now, records: map[string]execprotocol.Record{}, result: Result{SchemaVersion: 1, Command: "logs"}}
	if !execprotocol.ValidCommandID(commandID) || scope.Validate() != nil {
		return r.finish("invalid", "invalid", 2, "logs requires a valid command ID and trusted account, region, deployment and owner scope")
	}
	r.result.CommandID = commandID
	if r.now == nil {
		r.now = time.Now
	}
	if err := ctx.Err(); err != nil {
		return r.stopped(err)
	}
	if store == nil {
		return r.fail(recoveryIssue(execprotocol.ErrUnavailable))
	}
	final, modified, finalErr := r.read(ctx, "result")
	if finalErr == nil && r.accept(final, modified) {
		return r.final(final)
	}
	if err := ctx.Err(); err != nil {
		return r.stopped(err)
	}
	if finalErr != nil && (errors.Is(finalErr, execprotocol.ErrCorrupt) || errors.Is(finalErr, execprotocol.ErrUnsupportedSchema)) {
		r.invalid = recoveryIssue(finalErr)
	}
	var optional *observationIssue
	for _, kind := range []string{"outcome", "started", "acknowledgement", "request"} {
		record, modified, err := r.read(ctx, kind)
		if err == nil {
			r.accept(record, modified)
		} else if !errors.Is(err, execprotocol.ErrNotFound) {
			issue := recoveryIssue(err)
			if issue.outcome == "corrupt" || issue.outcome == "unsupported_schema" {
				r.invalid = issue
			} else {
				optional = issue
				r.warn(issue.code)
			}
		}
		if err := ctx.Err(); err != nil {
			return r.stopped(err)
		}
	}
	if r.invalid != nil {
		return r.fail(r.invalid)
	}
	var remoteIssue *observationIssue
	if api != nil && r.binding != nil && r.result.SSMCommandID != "" && !r.expired() {
		sub := Submission{Binding: *r.binding, SSMCommandID: r.result.SSMCommandID, State: "submitted"}
		r.result.SSM, remoteIssue = observeInvocation(ctx, api, sub)
		if remoteIssue != nil {
			r.warn(remoteIssue.code)
			if remoteIssue.outcome == "result_corrupt" {
				return r.fail(recoveryIssue(execprotocol.ErrCorrupt))
			}
		}
		if err := ctx.Err(); err != nil {
			return r.stopped(err)
		}
	}
	// Records may have appeared during the other metadata/SSM reads. Re-read the
	// final once, without polling for workload completion or retrying dispatch.
	final, modified, finalErr = r.read(ctx, "result")
	if finalErr == nil && r.accept(final, modified) {
		return r.final(final)
	}
	if err := ctx.Err(); err != nil {
		return r.stopped(err)
	}
	if r.invalid != nil {
		return r.fail(r.invalid)
	}
	if finalErr != nil && !errors.Is(finalErr, execprotocol.ErrNotFound) {
		return r.fail(recoveryIssue(finalErr))
	}
	if optional != nil {
		return r.fail(optional)
	}
	if r.expired() {
		return r.finish("expired", "expired", 1, "the command result retention deadline has passed")
	}
	if r.result.Workload != nil {
		return r.finish("incomplete", "incomplete", 1, "the workload outcome is recorded but final output publication is not established")
	}
	if remoteIssue != nil {
		outcome := "unavailable"
		if remoteIssue.code == "ssm_access_denied" {
			outcome = "access_denied"
		}
		return r.finish(outcome, remoteIssue.code, 1, remoteIssue.message)
	}
	if r.result.SSM != nil && !invocationTerminal(r.result.SSM.State) {
		return r.finish("pending", "pending", 1, "the remote invocation is pending or running; complete output publication is not established")
	}
	if r.result.DurableState == DurableStarted && r.result.SSM == nil {
		return r.finish("pending", "pending", 1, "the workload start is recorded; execution and output publication are not finalized")
	}
	if r.binding != nil {
		return r.finish("execution_unknown", "execution_unknown", 1, "command metadata exists but no durable workload outcome is recorded")
	}
	return r.finish("missing_or_expired", "missing_or_expired", 1, "no command metadata is available in the configured storage scope; verify the original account, region, deployment, owner and result storage configuration")
}
