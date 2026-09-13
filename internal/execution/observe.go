package execution

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
)

// InvocationState describes only the SSM wrapper. In particular, succeeded is
// not a workload exit or a guarantee that complete output is available.
type InvocationState string

const (
	InvocationPending             InvocationState = "pending"
	InvocationRunning             InvocationState = "running"
	InvocationDelayed             InvocationState = "delayed"
	InvocationSucceeded           InvocationState = "succeeded"
	InvocationFailed              InvocationState = "failed"
	InvocationDeliveryTimeout     InvocationState = "delivery_timeout"
	InvocationRunnerTimeout       InvocationState = "runner_timeout"
	InvocationCancellationPending InvocationState = "cancellation_pending"
	InvocationCancelled           InvocationState = "cancelled"
	InvocationUndeliverable       InvocationState = "undeliverable"
	InvocationTerminated          InvocationState = "terminated"
	InvocationInvalidPlatform     InvocationState = "invalid_platform"
	InvocationDenied              InvocationState = "denied"
	InvocationUnknown             InvocationState = "unknown"
)

type DurableState string

const (
	DurablePending DurableState = "pending"
	DurableStarted DurableState = "started"
	DurableOutcome DurableState = "outcome"
	DurableFinal   DurableState = "final"
)

// SSMObservation contains only identity-verified, allowlisted status metadata.
// ResponseCode is the raw wrapper response, including -1; it is never copied to
// Workload.ExitCode. Standard output, standard error, URLs and SDK messages are
// deliberately excluded.
type SSMObservation struct {
	State         InvocationState `json:"state"`
	Status        string          `json:"status,omitempty"`
	StatusDetails string          `json:"status_details,omitempty"`
	ResponseCode  *int32          `json:"response_code,omitempty"`
	Code          string          `json:"code,omitempty"`
}

type InvocationAPI interface {
	GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

type ObserveOptions struct {
	PollInterval time.Duration
}

const (
	observationCallTimeout = 10 * time.Second
	maxObservationFailures = 6
	maxObservationInterval = 5 * time.Second
)

type observationIssue struct {
	outcome, code, message string
	retryable              bool
}

type observer struct {
	sub     Submission
	store   execprotocol.Store
	result  Result
	started *execprotocol.Record
	outcome *execprotocol.Record
}

func (o *observer) finish(outcome, code string, exit int, message string) Result {
	o.result.Outcome, o.result.Code, o.result.ExitCode, o.result.Message = outcome, code, exit, message
	o.result.OK = outcome == "remote_exit" && exit == 0
	return o.result
}

func (o *observer) stopped(err error) Result {
	if errors.Is(err, context.Canceled) {
		return o.finish("interrupted", "interrupted", 4, "observation stopped; the remote command may continue; recover using the command ID")
	}
	return o.finish("observation_timeout", "observation_timeout", 4, "observation deadline expired; the remote command may continue; recover using the command ID")
}

func (o *observer) warn(code string) {
	for _, old := range o.result.Warnings {
		if old == code {
			return
		}
	}
	o.result.Warnings = append(o.result.Warnings, code)
}

func corruptObservation() *observationIssue {
	return &observationIssue{outcome: "result_corrupt", code: "result_corrupt", message: "command metadata is invalid, disagrees with independently recorded evidence, or does not match the submitted identity"}
}

func storageObservationIssue(err error) *observationIssue {
	switch {
	case errors.Is(err, execprotocol.ErrCorrupt):
		return corruptObservation()
	case errors.Is(err, execprotocol.ErrUnsupportedSchema):
		return &observationIssue{outcome: "unsupported_schema", code: "unsupported_schema", message: "the command result uses an unsupported schema version"}
	case errors.Is(err, execprotocol.ErrCredentialsExpired):
		return &observationIssue{outcome: "api_failed", code: "credentials_expired", message: "AWS credentials expired; refresh the selected profile and recover using the command ID"}
	case errors.Is(err, execprotocol.ErrCredentialsInvalid):
		return &observationIssue{outcome: "api_failed", code: "credentials_invalid", message: "AWS credentials were rejected; verify the selected profile and recover using the command ID"}
	case errors.Is(err, execprotocol.ErrCredentialsUnavailable):
		return &observationIssue{outcome: "api_failed", code: "credentials_unavailable", message: "AWS credentials could not be refreshed; sign in with the selected profile and recover using the command ID"}
	case errors.Is(err, execprotocol.ErrDenied):
		return &observationIssue{outcome: "api_failed", code: "result_access_denied", message: "durable result access was denied; verify the selected profile and scoped storage permissions"}
	case errors.Is(err, execprotocol.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return &observationIssue{outcome: "api_failed", code: "result_unavailable", message: "durable result reads remain unavailable; check connectivity and recover using the command ID", retryable: true}
	default:
		return &observationIssue{outcome: "api_failed", code: "result_api_failed", message: "cannot read durable command metadata; verify credentials, storage permissions and connectivity"}
	}
}

func sameRetention(a, b execprotocol.Record) bool {
	return a.SubmittedAt == b.SubmittedAt && a.ExpiresAt == b.ExpiresAt
}

func sameStreamEvidence(a, b execprotocol.Stream) bool {
	return a.Key == b.Key && a.Bytes == b.Bytes && a.SHA256 == b.SHA256
}

func sameOutcome(a, b execprotocol.Record) bool {
	return sameRetention(a, b) && a.FinishedAt == b.FinishedAt && reflect.DeepEqual(a.Workload, b.Workload) && a.Capture == b.Capture &&
		sameStreamEvidence(a.Streams.Stdout, b.Streams.Stdout) && sameStreamEvidence(a.Streams.Stderr, b.Streams.Stderr)
}

func (o *observer) validTimeline(record execprotocol.Record) bool {
	if o.started == nil {
		return true
	}
	started, _ := execprotocol.ParseTimestamp(o.started.StartedAt)
	finished, _ := execprotocol.ParseTimestamp(record.FinishedAt)
	return sameRetention(*o.started, record) && !finished.Before(started)
}

func (o *observer) read(ctx context.Context, kind string) (execprotocol.Record, error) {
	call, cancel := context.WithTimeout(ctx, observationCallTimeout)
	defer cancel()
	r, _, err := execprotocol.ReadRecord(call, o.store, execprotocol.ObjectKey(o.sub.Binding.Scope, o.sub.Binding.CommandID, kind+".json"))
	if err == nil && (r.Kind != kind || r.Binding != o.sub.Binding || r.SSMCommandID != o.sub.SSMCommandID) {
		return execprotocol.Record{}, execprotocol.ErrCorrupt
	}
	return r, err
}

func (o *observer) attach(record execprotocol.Record, state DurableState) {
	o.result.DurableState = state
	o.result.SubmittedAt, o.result.ExpiresAt = record.SubmittedAt, record.ExpiresAt
	if record.Kind == "started" {
		o.result.StartedAt = record.StartedAt
		return
	}
	o.result.FinishedAt = record.FinishedAt
	o.result.Workload, o.result.Capture, o.result.Streams = record.Workload, record.Capture, record.Streams
	o.result.Publication = record.Publication
	if record.Kind == "outcome" {
		o.result.Publication = "pending"
	}
}

// durable reads only metadata. Optional intermediate read failures do not defeat
// a standalone valid final record. Observed corruption and contradictions do;
// only independently verified outcome evidence survives a corrupt final.
func (o *observer) durable(ctx context.Context) (bool, *observationIssue) {
	var optional *observationIssue
	var invalid *observationIssue
	for _, kind := range []string{"started", "outcome"} {
		if (kind == "started" && o.started != nil) || (kind == "outcome" && o.outcome != nil) {
			continue
		}
		if ctx.Err() != nil {
			return false, nil
		}
		record, err := o.read(ctx, kind)
		if errors.Is(err, execprotocol.ErrNotFound) {
			continue
		}
		if err != nil {
			issue := storageObservationIssue(err)
			if issue.outcome == "result_corrupt" || issue.outcome == "unsupported_schema" {
				invalid = issue
			} else {
				optional = issue
			}
			continue
		}
		if kind == "started" {
			o.started = &record
			o.result.StartedAt = record.StartedAt
			if o.outcome == nil {
				o.attach(record, DurableStarted)
			} else if !o.validTimeline(*o.outcome) {
				invalid = corruptObservation()
			}
		} else {
			// The binding and record were validated independently. Keep this
			// workload evidence even if another record later contradicts it.
			o.outcome = &record
			o.attach(record, DurableOutcome)
			if !o.validTimeline(record) {
				invalid = corruptObservation()
			}
		}
	}
	if ctx.Err() != nil {
		return false, invalid
	}
	final, err := o.read(ctx, "result")
	if err == nil {
		if !o.validTimeline(final) || (o.outcome != nil && !sameOutcome(*o.outcome, final)) {
			invalid = corruptObservation()
		}
		if invalid != nil {
			return false, invalid
		}
		if optional != nil {
			o.warn(optional.code)
		}
		o.attach(final, DurableFinal)
		o.result = completedRecord(o.result, final)
		return true, nil
	}
	if invalid != nil {
		return false, invalid
	}
	if !errors.Is(err, execprotocol.ErrNotFound) {
		return false, storageObservationIssue(err)
	}
	if optional != nil {
		return false, optional
	}
	if o.result.ExpiresAt != "" {
		expires, _ := execprotocol.ParseTimestamp(o.result.ExpiresAt)
		if !time.Now().Before(expires) {
			return false, &observationIssue{outcome: "expired", code: "expired", message: "the command result retention deadline has passed"}
		}
	}
	return false, nil
}

// Observe follows one acknowledged invocation and its durable metadata. It has
// no dispatch or cancellation API and performs no mutations. Each API call and
// consecutive transient failure run is bounded, in addition to the caller's
// independent observation deadline. Missing/pending/delayed invocations remain
// observable until that deadline, without becoming fabricated workload exits.
func Observe(ctx context.Context, store execprotocol.Store, api InvocationAPI, sub Submission, options ObserveOptions) Result {
	o := observer{sub: sub, store: store, result: Result{SchemaVersion: 1, Command: "exec", CommandID: sub.Binding.CommandID, SSMCommandID: sub.SSMCommandID, InstanceID: sub.Binding.InstanceID, SubmissionState: sub.State, DurableState: DurablePending}}
	if sub.Binding.Validate() != nil || !execprotocol.ValidSSMCommandID(sub.SSMCommandID) {
		return o.finish("result_corrupt", "result_corrupt", 1, "cannot verify the submitted command identity")
	}
	if store == nil {
		return o.finish("api_failed", "result_api_failed", 1, "cannot read durable command metadata; verify credentials, storage permissions and connectivity")
	}
	interval := options.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	if interval > maxObservationInterval {
		interval = maxObservationInterval
	}
	storageFailures, ssmFailures := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return o.stopped(err)
		}
		complete, issue := o.durable(ctx)
		if complete {
			return o.result
		}
		if err := ctx.Err(); err != nil {
			return o.stopped(err)
		}
		if issue != nil {
			o.warn(issue.code)
			storageFailures++
			if !issue.retryable || storageFailures >= maxObservationFailures {
				return o.finish(issue.outcome, issue.code, 1, issue.message)
			}
		} else {
			storageFailures = 0
		}
		if api != nil {
			observation, remoteIssue := observeInvocation(ctx, api, sub)
			if observation != nil {
				o.result.SSM = observation
			}
			if err := ctx.Err(); err != nil {
				return o.stopped(err)
			}
			if remoteIssue != nil {
				o.warn(remoteIssue.code)
				if remoteIssue.outcome == "result_corrupt" {
					return o.finish(remoteIssue.outcome, remoteIssue.code, 1, remoteIssue.message)
				}
				ssmFailures++
			} else {
				ssmFailures = 0
			}
			terminal := observation != nil && invocationTerminal(observation.State)
			failed := remoteIssue != nil && (!remoteIssue.retryable || ssmFailures >= maxObservationFailures)
			if terminal || failed {
				// A terminal or failed optional SSM observation must not hide a
				// result published while that call was in flight. Re-read durable
				// records before choosing an SSM-only classification.
				complete, refreshedIssue := o.durable(ctx)
				if complete {
					return o.result
				}
				if err := ctx.Err(); err != nil {
					return o.stopped(err)
				}
				if refreshedIssue != nil {
					o.warn(refreshedIssue.code)
					if !refreshedIssue.retryable {
						return o.finish(refreshedIssue.outcome, refreshedIssue.code, 1, refreshedIssue.message)
					}
					// Required metadata remains unavailable, so SSM cannot prove
					// a workload result. Keep retrying storage within its budget.
				} else if failed {
					return o.finish(remoteIssue.outcome, remoteIssue.code, 1, remoteIssue.message)
				} else {
					return o.wrapperFinished(observation.State)
				}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return o.stopped(ctx.Err())
		case <-timer.C:
		}
		if interval >= maxObservationInterval/2 {
			interval = maxObservationInterval
		} else {
			interval *= 2
		}
	}
}

func (o *observer) wrapperFinished(state InvocationState) Result {
	if o.result.Workload != nil {
		if o.result.Workload.Status == "execution_timeout" {
			return o.finish("execution_timeout", "execution_timeout", 4, "the remote workload exceeded its execution deadline; complete publication is not established")
		}
		return o.finish("result_incomplete", "result_incomplete", 1, "the workload outcome is recorded but complete capture or publication is not established")
	}
	switch state {
	case InvocationDeliveryTimeout:
		return o.finish("delivery_timeout", "delivery_timeout", 4, "SSM reports delivery timeout; no workload completion is recorded")
	case InvocationRunnerTimeout:
		return o.finish("runner_timeout", "runner_timeout", 4, "SSM reports the runner's hard deadline expired; the workload exit is unknown")
	case InvocationCancelled:
		return o.finish("cancelled", "cancelled", 4, "SSM reports the wrapper cancelled; remote workload termination is not independently established")
	default:
		return o.finish("execution_unknown", "execution_unknown", 1, "SSM cannot establish a durable workload result; recover using the command ID")
	}
}

func invocationTerminal(state InvocationState) bool {
	switch state {
	case InvocationPending, InvocationRunning, InvocationDelayed, InvocationCancellationPending:
		return false
	default:
		return true
	}
}

func observeInvocation(ctx context.Context, api InvocationAPI, sub Submission) (*SSMObservation, *observationIssue) {
	call, cancel := context.WithTimeout(ctx, observationCallTimeout)
	defer cancel()
	out, err := api.GetCommandInvocation(call, &ssm.GetCommandInvocationInput{CommandId: aws.String(sub.SSMCommandID), InstanceId: aws.String(sub.Binding.InstanceID), PluginName: aws.String(sub.Binding.Document.Step)}, func(options *ssm.Options) {
		options.Retryer, options.RetryMaxAttempts = aws.NopRetryer{}, 1
		options.Credentials = execprotocol.SanitizedCredentials(options.Credentials)
	})
	if err != nil {
		return nil, invocationIssue(err)
	}
	if out == nil || aws.ToString(out.CommandId) != sub.SSMCommandID || aws.ToString(out.InstanceId) != sub.Binding.InstanceID || aws.ToString(out.DocumentName) != sub.Binding.Document.Name || aws.ToString(out.DocumentVersion) != sub.Binding.Document.Version || aws.ToString(out.PluginName) != sub.Binding.Document.Step {
		return nil, corruptObservation()
	}
	return classifyInvocation(out), nil
}

func invocationIssue(err error) *observationIssue {
	for _, sentinel := range []error{execprotocol.ErrCredentialsExpired, execprotocol.ErrCredentialsInvalid, execprotocol.ErrCredentialsUnavailable} {
		if errors.Is(err, sentinel) {
			return storageObservationIssue(err)
		}
	}
	if errors.Is(err, execprotocol.ErrDenied) {
		return &observationIssue{outcome: "api_failed", code: "ssm_access_denied", message: "SSM observation was denied; verify the selected profile and scoped invocation permissions"}
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "InvocationDoesNotExist":
			return nil // SSM is eventually consistent; absence is not failure.
		case "ExpiredToken", "ExpiredTokenException", "RequestExpired", "TokenRefreshRequired":
			return storageObservationIssue(execprotocol.ErrCredentialsExpired)
		case "InvalidClientTokenId", "UnrecognizedClientException", "InvalidSignatureException", "SignatureDoesNotMatch", "InvalidAccessKeyId", "InvalidToken", "AuthFailure", "RequestTimeTooSkewed":
			return storageObservationIssue(execprotocol.ErrCredentialsInvalid)
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
			return &observationIssue{outcome: "api_failed", code: "ssm_access_denied", message: "SSM observation was denied; verify the selected profile and scoped invocation permissions"}
		case "Throttling", "ThrottlingException", "TooManyRequestsException", "RequestLimitExceeded", "InternalServerError", "InternalFailure", "ServiceUnavailable", "ServiceUnavailableException", "RequestTimeout", "RequestTimeoutException":
			return &observationIssue{outcome: "api_failed", code: "ssm_unavailable", message: "SSM observation remains unavailable; check connectivity and recover using the command ID", retryable: true}
		default:
			if api.ErrorFault() != smithy.FaultServer {
				return &observationIssue{outcome: "api_failed", code: "ssm_api_failed", message: "SSM rejected invocation observation; verify the selected profile, command identity and permissions"}
			}
		}
	}
	return &observationIssue{outcome: "api_failed", code: "ssm_unavailable", message: "SSM observation remains unavailable; check connectivity and recover using the command ID", retryable: true}
}

func classifyInvocation(out *ssm.GetCommandInvocationOutput) *SSMObservation {
	observation := &SSMObservation{State: InvocationUnknown, ResponseCode: aws.Int32(out.ResponseCode), Code: "invocation_state_unknown"}
	status, details := string(out.Status), aws.ToString(out.StatusDetails)
	// Unknown service text is never returned verbatim. Require a recognized,
	// compatible pair before interpreting the wrapper state.
	statusAllowed := map[string]bool{"Pending": true, "InProgress": true, "Delayed": true, "Success": true, "Cancelled": true, "TimedOut": true, "Failed": true, "Cancelling": true}
	if statusAllowed[status] {
		observation.Status = status
	}
	detailState := map[string]InvocationState{
		"Pending": InvocationPending, "In Progress": InvocationRunning, "InProgress": InvocationRunning, "Delayed": InvocationDelayed,
		"Success": InvocationSucceeded, "Failed": InvocationFailed, "Delivery Timed Out": InvocationDeliveryTimeout, "DeliveryTimedOut": InvocationDeliveryTimeout,
		"Execution Timed Out": InvocationRunnerTimeout, "ExecutionTimedOut": InvocationRunnerTimeout, "Cancelling": InvocationCancellationPending,
		"Cancelled": InvocationCancelled, "Undeliverable": InvocationUndeliverable, "Terminated": InvocationTerminated,
		"Invalid Platform": InvocationInvalidPlatform, "InvalidPlatform": InvocationInvalidPlatform, "Access Denied": InvocationDenied, "AccessDenied": InvocationDenied,
	}
	state, known := detailState[details]
	if known {
		observation.StatusDetails = details
	}
	compatible := map[InvocationState]string{
		InvocationPending: "Pending", InvocationRunning: "InProgress", InvocationDelayed: "Delayed", InvocationSucceeded: "Success", InvocationFailed: "Failed",
		InvocationDeliveryTimeout: "TimedOut", InvocationRunnerTimeout: "TimedOut", InvocationCancellationPending: "Cancelling", InvocationCancelled: "Cancelled",
		InvocationUndeliverable: "Failed", InvocationTerminated: "Cancelled", InvocationInvalidPlatform: "Failed", InvocationDenied: "Failed",
	}
	if known && compatible[state] == status {
		observation.State, observation.Code = state, ""
	}
	return observation
}
