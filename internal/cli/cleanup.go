package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/JosephWest2/cloud_dev/internal/expirycleanup"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/aws/aws-sdk-go-v2/aws"
)

const cleanupTimeout = 165 * time.Second

type cleanupService interface {
	Run(context.Context, bool) (expiry.Result, error)
}
type cleanupDependencies struct {
	LoadAWS func(context.Context, config.Config) (aws.Config, error)
	New     func(expiry.Scope, aws.Config, expiry.Clock, expiry.Sink, expirycleanup.Limits) (cleanupService, error)
	Clock   expiry.Clock
}
type cleanupRunner func(context.Context, string, config.Overrides, bool, io.Writer) expiry.Result

func cleanupFailure(scope expiry.Scope, dry bool, code, message string, exit int) expiry.Result {
	return expiry.Result{SchemaVersion: 1, Command: "cleanup", Scope: scope, DryRun: dry, Code: code, Message: message, ExitCode: exit, Instances: []expiry.Outcome{}, Errors: []expiry.Problem{}}
}

func runCleanup(ctx context.Context, path string, overrides config.Overrides, dry bool, diagnostics io.Writer, deps cleanupDependencies) expiry.Result {
	// Bound credentials and setup as well as the service, even for --timeout 5m.
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	c, err := config.LoadCleanup(path, overrides)
	if err != nil {
		return cleanupFailure(expiry.Scope{}, dry, "cleanup_invalid", err.Error(), 2)
	}
	scope := expiry.Scope{Account: c.ExpectedAccount, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner}
	fail := func(code, message string, exit int) expiry.Result {
		if ctx.Err() != nil {
			code, message, exit = "cleanup_interrupted", "Cleanup deadline reached or interrupted; inspect retained IDs and rerun cleanup.", 4
		}
		return cleanupFailure(scope, dry, code, message, exit)
	}
	if ctx.Err() != nil {
		return fail("", "", 4)
	}
	if deps.LoadAWS == nil {
		deps.LoadAWS = identity.Load
	}
	if deps.Clock == nil {
		deps.Clock = expiry.SystemClock{}
	}
	if deps.New == nil {
		deps.New = func(s expiry.Scope, a aws.Config, clock expiry.Clock, sink expiry.Sink, limits expirycleanup.Limits) (cleanupService, error) {
			return expirycleanup.NewAWS(s, a, clock, sink, limits)
		}
	}
	a, err := deps.LoadAWS(ctx, c)
	if err != nil {
		return fail("cleanup_failed", "Cannot load AWS credentials; check the selected profile and refresh its credentials.", 1)
	}
	if ctx.Err() != nil {
		return fail("", "", 4)
	}
	output, ok := diagnostics.(*cleanupOutput)
	if !ok {
		output = newCleanupOutput(diagnostics)
	}
	sink := &cleanupSink{output: output}
	svc, err := deps.New(scope, a, deps.Clock, sink, expirycleanup.Limits{})
	if err != nil {
		return fail("cleanup_invalid", "Cannot construct cleanup service for the selected scope and credentials.", 2)
	}
	result, _ := svc.Run(ctx, dry)
	return result
}

// The gate bounds concurrent writes and goroutines. A blocked arbitrary Writer
// cannot be canceled by Go, but its acknowledgement can: no other write starts
// until it completes, and an unacknowledged event never grants dispatch. Buffers
// are owned by the writer goroutine. The caller's writer is never closed.
type cleanupOutput struct {
	writer io.Writer
	gate   chan struct{}
}

func newCleanupOutput(w io.Writer) *cleanupOutput {
	return &cleanupOutput{writer: w, gate: make(chan struct{}, 1)}
}
func (w *cleanupOutput) Write(data []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.write(ctx, data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (w *cleanupOutput) write(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case w.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-w.gate
		return err
	}
	copy := append([]byte(nil), data...)
	done := make(chan error, 1)
	go func() {
		var err error
		if w.writer == nil {
			err = io.ErrClosedPipe
		} else {
			_, err = (completeOutput{w.writer}).Write(copy)
		}
		done <- err
		<-w.gate
	}()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type cleanupSink struct{ output *cleanupOutput }

func (s *cleanupSink) Emit(ctx context.Context, event expiry.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.output.write(ctx, append(data, '\n'))
}

func emitCleanup(r expiry.Result, jsonMode bool, stdout, stderr io.Writer) int {
	if r.Instances == nil {
		r.Instances = []expiry.Outcome{}
	}
	if r.Errors == nil {
		r.Errors = []expiry.Problem{}
	}
	for i := range r.Instances {
		if r.Instances[i].Volumes == nil {
			r.Instances[i].Volumes = []expiry.Volume{}
		}
		if r.Instances[i].Errors == nil {
			r.Instances[i].Errors = []expiry.Problem{}
		}
	}
	var err error
	if jsonMode {
		err = json.NewEncoder(completeOutput{stdout}).Encode(r)
	} else {
		err = writeCleanupText(completeOutput{stdout}, r)
	}
	if err != nil {
		// A failed stdout cannot be repaired into valid JSON. Preserve one complete
		// envelope on stderr so identities and outcomes remain recoverable.
		fmt.Fprintln(stderr, "devbox: cannot write cleanup result; complete recovery result follows")
		_ = json.NewEncoder(completeOutput{stderr}).Encode(r)
		return 1
	}
	return r.ExitCode
}

func writeCleanupText(w io.Writer, r expiry.Result) error {
	var failure error
	write := func(format string, args ...any) {
		if failure == nil {
			_, failure = fmt.Fprintf(w, format, args...)
		}
	}
	write("%s: %s\n", r.Code, cleanupExplanation(r.Code, r.Message))
	write("account=%s region=%s deployment=%s owner=%s dry_run=%t evaluated_at=%s completed_at=%s\n", r.Scope.Account, r.Scope.Region, r.Scope.Deployment, r.Scope.Owner, r.DryRun, r.EvaluatedAt, r.CompletedAt)
	write("scan_complete=%t complete=%t scanned=%d candidates=%d terminated=%d cleaned=%d\n", r.ScanComplete, r.Complete, r.ScannedCount, r.CandidateCount, r.TerminatedCount, r.CleanedCount)
	if r.DryRun {
		write("Advisory snapshot: rerunning cleanup performs fresh discovery and exact-ID checks.\n")
	}
	for _, o := range r.Instances {
		expires := "unavailable"
		if o.ExpiresAt != nil {
			expires = *o.ExpiresAt
		}
		write("%s ec2=%s expires_at=%s evaluated_at=%s eligible=%t reason=%s status=%s root_deletion=%s\n", o.InstanceID, o.State, expires, o.EvaluatedAt, o.Eligible, o.Reason, o.Status, o.RootDeletion)
		write("  %s\n", cleanupExplanation(string(o.Reason), string(o.Reason)))
		for _, v := range o.Volumes {
			flag := "unknown"
			if v.DeleteOnTermination != nil {
				flag = fmt.Sprint(*v.DeleteOnTermination)
			}
			write("  volume=%s device=%q root=%t delete_on_termination=%s deletion=%s\n", v.ID, v.Device, v.Root, flag, v.Deletion)
		}
		for _, p := range o.Errors {
			write("  error=%s: %s\n", p.Code, cleanupExplanation(p.Code, p.Message))
		}
	}
	for _, p := range r.Errors {
		write("error=%s resource=%s: %s\n", p.Code, p.ResourceID, cleanupExplanation(p.Code, p.Message))
	}
	return failure
}

func cleanupExplanation(code, fallback string) string {
	switch code {
	case "cleanup_complete":
		return "Cleanup evaluation and required work completed; termination and root deletion are reported separately."
	case "cleanup_no_candidates":
		return "No expired candidates; future and legacy workers remain available."
	case "cleanup_partial":
		return "Some work completed; inspect per-instance failures and safely rerun cleanup."
	case "cleanup_failed":
		return "Cleanup could not complete; inspect scope and per-instance errors before rerunning."
	case "cleanup_interrupted":
		return "Cleanup timed out or was interrupted; retain known IDs and rerun cleanup to observe current state."
	case "expired":
		return "The request deadline has passed."
	case "expiry_future":
		return "The deadline has not arrived; this worker is skipped."
	case "expiry_missing":
		return "No expiry is recorded; use ls and explicit down for deliberate legacy teardown."
	case "expiry_invalid", "expiry_duplicate":
		return "Expiry is malformed or ambiguous; inspect with ls; explicit down remains available."
	case "scope_mismatch", "identity_mismatch":
		return "AWS evidence differs from the trusted scope; no termination is authorized."
	case "already_terminated":
		return "EC2 is terminated; missing historical root mappings do not prove volume deletion."
	case "already_terminating":
		return "Termination is already in progress; observe the exact instance and root volumes."
	case "root_volume_retained":
		return "The root is retained; automatic cleanup skips it. Inspect with ls and use explicit down deliberately."
	case "root_volume_unverified":
		return "Root deletion is unverified; inspect the captured exact instance and volume IDs."
	case "identity_unverified":
		return "Cannot verify the selected AWS identity; refresh its credentials and check expected_account before retrying."
	case "scan_incomplete":
		return "Discovery was incomplete; no candidate set authorizes termination. Retain known IDs and rerun cleanup."
	case "termination_denied", "termination_protected":
		return "Termination was denied or protected; inspect scoped permissions and instance protection before retrying."
	case "termination_unknown", "termination_unresolved":
		return "Termination is unverified; retain exact IDs and rerun cleanup to observe current state."
	case "resource_unverified", "resource_invalid", "resource_changed":
		return "Exact resource evidence is missing, invalid or changed; no new termination is authorized for this observation."
	case "expiry_changed":
		return "The deadline changed after discovery; this run skips the worker and a later cleanup evaluates it afresh."
	case "evidence_unavailable":
		return "Cleanup evidence could not be acknowledged; inspect retained IDs and restore diagnostic output before rerunning."
	}
	if fallback == "" {
		return "No additional diagnostic is available."
	}
	return fallback
}
