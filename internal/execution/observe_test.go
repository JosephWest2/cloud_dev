package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

type invocationFunc func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)

func (f invocationFunc) GetCommandInvocation(ctx context.Context, in *ssm.GetCommandInvocationInput, opts ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	return f(ctx, in, opts...)
}

func invocation(sub Submission, status, details string, response int32) *ssm.GetCommandInvocationOutput {
	return &ssm.GetCommandInvocationOutput{CommandId: aws.String(sub.SSMCommandID), InstanceId: aws.String(sub.Binding.InstanceID), DocumentName: aws.String(sub.Binding.Document.Name), DocumentVersion: aws.String(sub.Binding.Document.Version), PluginName: aws.String(sub.Binding.Document.Step), Status: ssmtypes.CommandInvocationStatus(status), StatusDetails: aws.String(details), ResponseCode: response, StandardOutputContent: aws.String("SECRET workload stdout"), StandardErrorContent: aws.String("SECRET workload stderr"), StandardOutputUrl: aws.String("https://SECRET.invalid/")}
}

func intermediateRecords(final execprotocol.Record) (execprotocol.Record, execprotocol.Record) {
	started := execprotocol.Record{SchemaVersion: 1, Kind: "started", Binding: final.Binding, SSMCommandID: final.SSMCommandID, SubmittedAt: final.SubmittedAt, ExpiresAt: final.ExpiresAt, StartedAt: final.SubmittedAt}
	outcome := final
	outcome.Kind, outcome.Publication, outcome.FinalizedAt = "outcome", "", ""
	streams := *outcome.Streams
	streams.Stdout.Upload, streams.Stderr.Upload = "pending", "pending"
	outcome.Streams = &streams
	return started, outcome
}

func metadataStore(t *testing.T, records ...execprotocol.Record) *finalStore {
	t.Helper()
	values := map[string][]byte{}
	for _, record := range records {
		values[record.Kind+".json"] = encodedFinal(t, record)
	}
	return &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
		leaf := key[strings.LastIndex(key, "/")+1:]
		if data, ok := values[leaf]; ok {
			return finalObject(data), nil
		}
		return execprotocol.Object{}, execprotocol.ErrNotFound
	}}
}

func quickObserve(ctx context.Context, store execprotocol.Store, api InvocationAPI, sub Submission) Result {
	return Observe(ctx, store, api, sub, ObserveOptions{PollInterval: time.Nanosecond})
}

func assertNoUntrustedContent(t *testing.T, result Result) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil || strings.Contains(string(data), "SECRET") {
		t.Fatalf("untrusted service data reached result: %s", data)
	}
}

func TestObserveDurableExitAndPublicationPrecedence(t *testing.T) {
	for _, code := range []int{0, 1, 2, 4, 255} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			final, sub := finalRecord()
			final.Workload.ExitCode = &code
			started, outcome := intermediateRecords(final)
			store := metadataStore(t, started, outcome, final)
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				t.Fatal("SSM queried after established durable final")
				return nil, nil
			})
			got := quickObserve(context.Background(), store, api, sub)
			assertFinalIdentity(t, got, sub)
			if got.Outcome != "remote_exit" || got.ExitCode != code || got.OK != (code == 0) || got.DurableState != DurableFinal || got.StartedAt != started.StartedAt || got.SubmittedAt != final.SubmittedAt || got.ExpiresAt != final.ExpiresAt || got.FinishedAt != final.FinishedAt {
				t.Fatalf("incorrect final result: %+v", got)
			}
			if len(store.reads) != 3 {
				t.Fatalf("metadata-only observation read unexpected keys: %v", store.reads)
			}
		})
	}
	for _, tc := range []struct {
		name, status, capture, publication, outcome string
		exit                                        int
	}{
		{"signal", "signaled", "complete", "complete", "remote_signal", 143},
		{"workload timeout", "execution_timeout", "incomplete", "incomplete", "execution_timeout", 4},
		{"incomplete publication", "exited", "complete", "incomplete", "result_incomplete", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final, sub := finalRecord()
			final.Workload.Status, final.Capture, final.Publication = tc.status, tc.capture, tc.publication
			if tc.status != "exited" {
				final.Workload.ExitCode, final.Workload.Signal = nil, aws.Int(15)
			}
			if tc.publication != "complete" {
				final.Streams.Stderr.Upload = "failed"
			}
			got := quickObserve(context.Background(), metadataStore(t, final), nil, sub)
			if got.Outcome != tc.outcome || got.ExitCode != tc.exit || !reflect.DeepEqual(got.Workload, final.Workload) {
				t.Fatalf("lost workload/publication priority: %+v", got)
			}
		})
	}
}

func TestObserveOptionalIntermediateFailuresCannotDefeatFinal(t *testing.T) {
	for _, err := range []error{execprotocol.ErrDenied, execprotocol.ErrUnavailable, execprotocol.ErrCredentialsExpired, execprotocol.ErrCredentialsInvalid, execprotocol.ErrCredentialsUnavailable, errors.New("SECRET optional API error")} {
		final, sub := finalRecord()
		data := encodedFinal(t, final)
		store := &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
			if strings.HasSuffix(key, "/result.json") {
				return finalObject(data), nil
			}
			return execprotocol.Object{}, err
		}}
		got := quickObserve(context.Background(), store, nil, sub)
		if !got.OK || got.Outcome != "remote_exit" || len(got.Warnings) != 1 {
			t.Fatalf("optional metadata failure defeated final: %+v", got)
		}
		assertNoUntrustedContent(t, got)
	}
}

func TestObserveEventualVisibilityAndTransientErrorsWithoutRedispatch(t *testing.T) {
	final, sub := finalRecord()
	code := 2
	final.Workload.ExitCode = &code
	started, outcome := intermediateRecords(final)
	steps := []struct {
		status, details string
		err             error
	}{
		{err: &ssmtypes.InvocationDoesNotExist{}},
		{status: "Pending", details: "Pending"},
		{err: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "SECRET"}},
		{status: "Delayed", details: "Delayed"},
		{err: errors.New("SECRET transport interrupted")},
		{status: "InProgress", details: "In Progress"},
		{status: "Success", details: "Success"},
	}
	calls, outcomeReads, startedReads := 0, 0, 0
	store := &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
		switch {
		case strings.HasSuffix(key, "/started.json"):
			startedReads++
			return finalObject(encodedFinal(t, started)), nil
		case strings.HasSuffix(key, "/outcome.json"):
			outcomeReads++
			if calls >= 6 {
				return finalObject(encodedFinal(t, outcome)), nil
			}
		case strings.HasSuffix(key, "/result.json"):
			if calls == len(steps) {
				return finalObject(encodedFinal(t, final)), nil
			}
		default:
			t.Fatalf("read workload bytes: %q", key)
		}
		return execprotocol.Object{}, execprotocol.ErrNotFound
	}}
	api := invocationFunc(func(_ context.Context, in *ssm.GetCommandInvocationInput, opts ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
		if aws.ToString(in.CommandId) != sub.SSMCommandID || aws.ToString(in.InstanceId) != sub.Binding.InstanceID || aws.ToString(in.PluginName) != "execute" {
			t.Fatalf("unscoped invocation lookup: %+v", in)
		}
		options := ssm.Options{}
		for _, apply := range opts {
			apply(&options)
		}
		if options.RetryMaxAttempts != 1 || options.Retryer == nil || options.Retryer.MaxAttempts() != 1 {
			t.Fatal("observer did not bound the actual SDK operation attempts")
		}
		if calls >= len(steps) {
			t.Fatal("retried a completed observation")
		}
		step := steps[calls]
		calls++
		if step.err != nil {
			return nil, step.err
		}
		return invocation(sub, step.status, step.details, 0), nil
	})
	got := quickObserve(context.Background(), store, api, sub)
	if got.Outcome != "remote_exit" || got.ExitCode != 2 || calls != len(steps) || got.SSM == nil || got.SSM.State != InvocationSucceeded || got.SSM.ResponseCode == nil || *got.SSM.ResponseCode != 0 || len(got.Warnings) != 1 || got.Warnings[0] != "ssm_unavailable" || startedReads != 1 || outcomeReads != 7 {
		t.Fatalf("eventual observation did not recover/cache metadata: %+v calls=%d started=%d outcome=%d", got, calls, startedReads, outcomeReads)
	}
	assertNoUntrustedContent(t, got)
}

func TestObserveWrapperStatesNeverBecomeWorkloadExit(t *testing.T) {
	for _, tc := range []struct {
		status, details, outcome string
		state                    InvocationState
		response                 int32
		exit                     int
	}{
		{"Success", "Success", "execution_unknown", InvocationSucceeded, 0, 1},
		{"Failed", "Failed", "execution_unknown", InvocationFailed, 255, 1},
		{"Failed", "Failed", "execution_unknown", InvocationFailed, -1, 1},
		{"TimedOut", "Delivery Timed Out", "delivery_timeout", InvocationDeliveryTimeout, -1, 4},
		{"TimedOut", "Execution Timed Out", "runner_timeout", InvocationRunnerTimeout, 137, 4},
		{"Cancelled", "Cancelled", "cancelled", InvocationCancelled, 137, 4},
		{"Failed", "Undeliverable", "execution_unknown", InvocationUndeliverable, -1, 1},
		{"Cancelled", "Terminated", "execution_unknown", InvocationTerminated, -1, 1},
		{"Failed", "Invalid Platform", "execution_unknown", InvocationInvalidPlatform, -1, 1},
		{"Failed", "Access Denied", "execution_unknown", InvocationDenied, -1, 1},
		{"SECRET new state", "SECRET details", "execution_unknown", InvocationUnknown, -1, 1},
		{"Success", "Execution Timed Out", "execution_unknown", InvocationUnknown, 0, 1},
		{"TimedOut", "", "execution_unknown", InvocationUnknown, -1, 1},
	} {
		t.Run(tc.status+"/"+tc.details+fmt.Sprint(tc.response), func(t *testing.T) {
			_, sub := finalRecord()
			calls := 0
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				calls++
				return invocation(sub, tc.status, tc.details, tc.response), nil
			})
			got := quickObserve(context.Background(), metadataStore(t), api, sub)
			if got.Outcome != tc.outcome || got.ExitCode != tc.exit || got.Workload != nil || got.OK || got.SSM == nil || got.SSM.State != tc.state || *got.SSM.ResponseCode != tc.response || calls != 1 {
				t.Fatalf("wrapper response became a workload result: %+v calls=%d", got, calls)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestObserveKnownOutcomeSurvivesWrapperFailureAndTimeout(t *testing.T) {
	for _, status := range []string{"exited", "execution_timeout"} {
		for _, wrapper := range []struct{ status, details string }{{"Success", "Success"}, {"Failed", "Failed"}, {"TimedOut", "Delivery Timed Out"}, {"TimedOut", "Execution Timed Out"}, {"Cancelled", "Cancelled"}} {
			final, sub := finalRecord()
			final.Workload = &execprotocol.Workload{Status: status}
			if status == "exited" {
				final.Workload.ExitCode = aws.Int(4)
			}
			_, outcome := intermediateRecords(final)
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				return invocation(sub, wrapper.status, wrapper.details, -1), nil
			})
			got := quickObserve(context.Background(), metadataStore(t, outcome), api, sub)
			wantOutcome, wantExit := "result_incomplete", 1
			if status == "execution_timeout" {
				wantOutcome, wantExit = "execution_timeout", 4
			}
			if got.Outcome != wantOutcome || got.ExitCode != wantExit || !reflect.DeepEqual(got.Workload, outcome.Workload) || got.DurableState != DurableOutcome || got.Publication != "pending" || got.SSM == nil {
				t.Fatalf("durable outcome lost to wrapper: %+v", got)
			}
		}
	}
}

func TestObservePendingAndCancellationRemainNonterminal(t *testing.T) {
	for _, wrapper := range []struct{ status, details string }{{"Pending", "Pending"}, {"InProgress", "In Progress"}, {"Delayed", "Delayed"}, {"Cancelling", "Cancelling"}} {
		t.Run(wrapper.status, func(t *testing.T) {
			_, sub := finalRecord()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				calls++
				if calls == 3 {
					cancel()
				}
				return invocation(sub, wrapper.status, wrapper.details, -1), nil
			})
			got := quickObserve(ctx, metadataStore(t), api, sub)
			if got.Outcome != "interrupted" || got.ExitCode != 4 || calls != 3 || got.SSM == nil || got.SSM.Status != wrapper.status || got.Workload != nil || got.OK {
				t.Fatalf("nonterminal state invented a remote termination: %+v calls=%d", got, calls)
			}
		})
	}
	_, sub := finalRecord()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
		return nil, &ssmtypes.InvocationDoesNotExist{}
	})
	got := Observe(ctx, metadataStore(t), api, sub, ObserveOptions{})
	if got.Outcome != "observation_timeout" || got.ExitCode != 4 || got.Workload != nil || got.SSM != nil {
		t.Fatalf("missing invocation became a completed exit: %+v", got)
	}
}

func TestObserveFinalWinsCancellationAndOptionalSSMRaces(t *testing.T) {
	for _, scenario := range []string{"cancel during final read", "SSM denied while final arrives", "SSM timeout while final arrives", "SSM cancelled while final arrives"} {
		t.Run(scenario, func(t *testing.T) {
			final, sub := finalRecord()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := scenario == "cancel during final read"
			store := &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
				if strings.HasSuffix(key, "/result.json") && ready {
					cancel()
					return finalObject(encodedFinal(t, final)), nil
				}
				return execprotocol.Object{}, execprotocol.ErrNotFound
			}}
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				ready = true
				switch scenario {
				case "SSM denied while final arrives":
					return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "SECRET"}
				case "SSM timeout while final arrives":
					return invocation(sub, "TimedOut", "Execution Timed Out", 137), nil
				case "SSM cancelled while final arrives":
					return invocation(sub, "Cancelled", "Cancelled", 137), nil
				default:
					t.Fatal("optional SSM call made after established final")
					return nil, nil
				}
			})
			got := quickObserve(ctx, store, api, sub)
			if !got.OK || got.Outcome != "remote_exit" || got.DurableState != DurableFinal {
				t.Fatalf("completion lost to observation race: %+v", got)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestObserveRejectsInvocationIdentityWithoutExposingIt(t *testing.T) {
	for _, field := range []string{"command", "instance", "document", "version", "step", "nil output"} {
		t.Run(field, func(t *testing.T) {
			final, sub := finalRecord()
			_, outcome := intermediateRecords(final)
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				out := invocation(sub, "Success", "Success", 255)
				switch field {
				case "command":
					out.CommandId = aws.String("SECRET")
				case "instance":
					out.InstanceId = aws.String("SECRET")
				case "document":
					out.DocumentName = aws.String("SECRET")
				case "version":
					out.DocumentVersion = aws.String("SECRET")
				case "step":
					out.PluginName = aws.String("SECRET")
				case "nil output":
					out = nil
				}
				return out, nil
			})
			got := quickObserve(context.Background(), metadataStore(t, outcome), api, sub)
			if got.Outcome != "result_corrupt" || got.SSM != nil || !reflect.DeepEqual(got.Workload, outcome.Workload) || got.DurableState != DurableOutcome {
				t.Fatalf("trusted another invocation or lost independent outcome: %+v", got)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestObserveOutcomeFinalDisagreementPreservesIndependentOutcome(t *testing.T) {
	for _, field := range []string{"workload", "capture", "retention", "finished", "bytes", "hash", "instance", "payload", "unsupported", "malformed"} {
		t.Run(field, func(t *testing.T) {
			final, sub := finalRecord()
			_, outcome := intermediateRecords(final)
			final.Workload = &execprotocol.Workload{Status: "exited", ExitCode: aws.Int(0)}
			switch field {
			case "workload":
				final.Workload.ExitCode = aws.Int(255)
			case "capture":
				final.Capture, final.Publication = "incomplete", "incomplete"
			case "retention":
				expires, _ := execprotocol.ParseTimestamp(final.ExpiresAt)
				final.ExpiresAt = execprotocol.Timestamp(expires.Add(24 * time.Hour))
			case "finished":
				final.FinishedAt = final.FinalizedAt
			case "bytes":
				final.Streams.Stdout.Bytes = 1
			case "hash":
				final.Streams.Stdout.Bytes, final.Streams.Stdout.SHA256 = 1, strings.Repeat("d", 64)
			case "instance":
				final.InstanceID = "i-87654321"
			case "payload":
				final.PayloadSHA256 = strings.Repeat("d", 64)
			}
			finalData := encodedFinal(t, final)
			want := "result_corrupt"
			if field == "unsupported" {
				finalData, want = []byte(`{"schema_version":2}`), "unsupported_schema"
			} else if field == "malformed" {
				finalData = []byte(`{"SECRET":`)
			}
			outcomeData := encodedFinal(t, outcome)
			store := &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
				if strings.HasSuffix(key, "/outcome.json") {
					return finalObject(outcomeData), nil
				}
				if strings.HasSuffix(key, "/result.json") {
					return finalObject(finalData), nil
				}
				return execprotocol.Object{}, execprotocol.ErrNotFound
			}}
			got := quickObserve(context.Background(), store, nil, sub)
			if got.Outcome != want || got.ExitCode != 1 || got.OK || !reflect.DeepEqual(got.Workload, outcome.Workload) || !reflect.DeepEqual(got.Streams, outcome.Streams) || got.DurableState != DurableOutcome || got.Publication != "pending" {
				t.Fatalf("contradictory final displaced independent outcome: %+v", got)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestObserveStartedAndOutcomeBindingAndTimeline(t *testing.T) {
	for _, kind := range []string{"started", "outcome"} {
		for _, mutation := range []string{"instance", "ssm", "retention", "timeline"} {
			t.Run(kind+"/"+mutation, func(t *testing.T) {
				final, sub := finalRecord()
				started, outcome := intermediateRecords(final)
				changed := &started
				if kind == "outcome" {
					changed = &outcome
				}
				switch mutation {
				case "instance":
					changed.InstanceID = "i-87654321"
				case "ssm":
					changed.SSMCommandID = "00000000-0000-0000-0000-000000000000"
				case "retention":
					expires, _ := execprotocol.ParseTimestamp(changed.ExpiresAt)
					changed.ExpiresAt = execprotocol.Timestamp(expires.Add(24 * time.Hour))
				case "timeline":
					if kind == "started" {
						started.StartedAt = final.FinalizedAt
					} else {
						outcome.FinishedAt = final.SubmittedAt
						started.StartedAt = final.FinishedAt
					}
				}
				got := quickObserve(context.Background(), metadataStore(t, started, outcome, final), nil, sub)
				if got.Outcome != "result_corrupt" || got.OK || got.DurableState == DurableFinal || (kind == "outcome" && (mutation == "instance" || mutation == "ssm") && got.Workload != nil) {
					t.Fatalf("untrusted intermediate metadata accepted: %+v", got)
				}
			})
		}
	}
}

func TestObserveRetryBudgetsAndActionableErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		err        error
		retry      bool
	}{
		{"expired", "credentials_expired", execprotocol.ErrCredentialsExpired, false},
		{"invalid", "credentials_invalid", execprotocol.ErrCredentialsInvalid, false},
		{"local credential helper", "credentials_unavailable", execprotocol.ErrCredentialsUnavailable, false},
		{"storage denied", "result_access_denied", execprotocol.ErrDenied, false},
		{"storage transport", "result_unavailable", execprotocol.ErrUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sub := finalRecord()
			finalReads := 0
			store := &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
				if strings.HasSuffix(key, "/result.json") {
					finalReads++
					return execprotocol.Object{}, tc.err
				}
				return execprotocol.Object{}, execprotocol.ErrNotFound
			}}
			got := quickObserve(context.Background(), store, nil, sub)
			wantReads := 1
			if tc.retry {
				wantReads = maxObservationFailures
			}
			if got.Outcome != "api_failed" || got.Code != tc.code || got.ExitCode != 1 || finalReads != wantReads {
				t.Fatalf("incorrect storage failure classification/budget: %+v reads=%d", got, finalReads)
			}
		})
	}
	for _, tc := range []struct {
		awsCode, code string
		retry         bool
	}{
		{"ExpiredTokenException", "credentials_expired", false}, {"InvalidClientTokenId", "credentials_invalid", false},
		{"AccessDeniedException", "ssm_access_denied", false}, {"InvalidPluginName", "ssm_api_failed", false},
		{"ThrottlingException", "ssm_unavailable", true}, {"InternalServerError", "ssm_unavailable", true},
	} {
		t.Run(tc.awsCode, func(t *testing.T) {
			_, sub := finalRecord()
			calls := 0
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				calls++
				return nil, &smithy.GenericAPIError{Code: tc.awsCode, Message: "SECRET credentials payload raw SDK error"}
			})
			got := quickObserve(context.Background(), metadataStore(t), api, sub)
			wantCalls := 1
			if tc.retry {
				wantCalls = maxObservationFailures
			}
			if got.Outcome != "api_failed" || got.Code != tc.code || got.ExitCode != 1 || got.Workload != nil || calls != wantCalls {
				t.Fatalf("incorrect SSM failure classification/budget: %+v calls=%d", got, calls)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestObserveExpiredAndInterruptedEvidence(t *testing.T) {
	t.Run("expired final", func(t *testing.T) {
		final, sub := finalRecord()
		submitted := time.Now().Add(-31 * 24 * time.Hour).UTC().Truncate(time.Second)
		final.SubmittedAt, final.ExpiresAt = execprotocol.Timestamp(submitted), execprotocol.Timestamp(submitted.Add(30*24*time.Hour))
		final.FinishedAt, final.FinalizedAt = final.SubmittedAt, final.SubmittedAt
		got := quickObserve(context.Background(), metadataStore(t, final), nil, sub)
		if got.OK || got.Outcome != "expired" || got.ExitCode != 1 || got.Workload == nil {
			t.Fatalf("expired final accepted: %+v", got)
		}
	})
	t.Run("cancel before first read", func(t *testing.T) {
		_, sub := finalRecord()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		store := metadataStore(t)
		got := quickObserve(ctx, store, nil, sub)
		if got.Outcome != "interrupted" || got.ExitCode != 4 || len(store.reads) != 0 {
			t.Fatalf("read or fabricated result after cancellation: %+v", got)
		}
	})
	t.Run("cancel after trustworthy outcome", func(t *testing.T) {
		final, sub := finalRecord()
		_, outcome := intermediateRecords(final)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
			if strings.HasSuffix(key, "/outcome.json") {
				cancel()
				return finalObject(encodedFinal(t, outcome)), nil
			}
			return execprotocol.Object{}, execprotocol.ErrNotFound
		}}
		got := quickObserve(ctx, store, nil, sub)
		if got.Outcome != "interrupted" || got.ExitCode != 4 || got.DurableState != DurableOutcome || !reflect.DeepEqual(got.Workload, outcome.Workload) || got.Publication != "pending" {
			t.Fatalf("interruption erased independently known outcome: %+v", got)
		}
	})
}

func TestObserveActualSDKBoundsReadsAndSanitizesCredentialRefresh(t *testing.T) {
	for _, scenario := range []string{"transport retry budget", "opaque credential helper failure", "expired credential helper failure"} {
		t.Run(scenario, func(t *testing.T) {
			_, sub := finalRecord()
			transport := &lostResponseTransport{}
			retrievals := 0
			client := ssm.NewFromConfig(aws.Config{Region: "us-east-2", RetryMaxAttempts: 5, HTTPClient: transport, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				retrievals++
				switch scenario {
				case "opaque credential helper failure":
					return aws.Credentials{}, errors.New("SECRET child stderr from credential process")
				case "expired credential helper failure":
					return aws.Credentials{}, &smithy.GenericAPIError{Code: "ExpiredToken", Message: "SECRET expired credential response"}
				default:
					return aws.Credentials{AccessKeyID: "TEST", SecretAccessKey: "TEST"}, nil
				}
			})})
			got := quickObserve(context.Background(), metadataStore(t), client, sub)
			wantCode, wantWire, wantRetrievals := "ssm_unavailable", maxObservationFailures, maxObservationFailures
			if scenario != "transport retry budget" {
				wantCode, wantWire, wantRetrievals = "credentials_unavailable", 0, 1
				if scenario == "expired credential helper failure" {
					wantCode = "credentials_expired"
				}
			}
			if got.Outcome != "api_failed" || got.Code != wantCode || transport.calls != wantWire || retrievals != wantRetrievals {
				t.Fatalf("actual SDK bypassed observer budget or credential sanitization: %+v wire=%d retrievals=%d", got, transport.calls, retrievals)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}
