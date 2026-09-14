package execution

import (
	"context"
	"errors"
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

func recoverMetadata(t *testing.T, final execprotocol.Record, records ...execprotocol.Record) *finalStore {
	t.Helper()
	values := map[string][]byte{}
	for _, record := range records {
		values[record.Kind+".json"] = encodedFinal(t, record)
	}
	submitted, _ := execprotocol.ParseTimestamp(final.SubmittedAt)
	return &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
		if !strings.HasPrefix(key, execprotocol.BasePrefix(final.Scope)+final.CommandID+"/") {
			t.Fatalf("untrusted lookup key: %s", key)
		}
		name := key[strings.LastIndex(key, "/")+1:]
		if name == "stdout" || name == "stderr" {
			t.Fatal("metadata recovery read workload bytes")
		}
		if value, ok := values[name]; ok {
			object := finalObject(value)
			object.LastModified = submitted
			return object, nil
		}
		return execprotocol.Object{}, execprotocol.ErrNotFound
	}}
}

func requestAndAck(final execprotocol.Record) (execprotocol.Record, execprotocol.Record) {
	request := execprotocol.Record{SchemaVersion: 1, Kind: "request", Binding: final.Binding, RetentionDays: 30}
	ack := execprotocol.Record{SchemaVersion: 1, Kind: "acknowledgement", Binding: final.Binding, SSMCommandID: final.SSMCommandID}
	return request, ack
}

func recoverResult(store execprotocol.Store, api InvocationAPI, final execprotocol.Record) Result {
	return Recover(context.Background(), store, api, final.Scope, final.CommandID, RecoverOptions{})
}

func TestRecoverStandaloneHistoricalFinalSeparatesRetrievalFromWorkload(t *testing.T) {
	for _, status := range []string{"exited", "signaled", "execution_timeout", "runner_setup_failed"} {
		t.Run(status, func(t *testing.T) {
			final, _ := finalRecord()
			now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
			submitted := now.Add(-60 * 24 * time.Hour)
			final.SubmittedAt, final.ExpiresAt = execprotocol.Timestamp(submitted), execprotocol.Timestamp(submitted.Add(365*24*time.Hour))
			final.FinishedAt, final.FinalizedAt = execprotocol.Timestamp(submitted.Add(time.Second)), execprotocol.Timestamp(submitted.Add(2*time.Second))
			final.Document.Version, final.RunnerSHA256 = "73", strings.Repeat("d", 64)
			final.Workload = &execprotocol.Workload{Status: status}
			if status == "exited" {
				final.Workload.ExitCode = aws.Int(255)
			}
			if status == "signaled" || status == "execution_timeout" {
				final.Workload.Signal = aws.Int(15)
			}
			store := recoverMetadata(t, final, final)
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				t.Fatal("completed historical recovery depends on SSM history")
				return nil, nil
			})
			got := Recover(context.Background(), store, api, final.Scope, final.CommandID, RecoverOptions{Now: func() time.Time { return now }})
			if got.Command != "logs" || !got.OK || got.ExitCode != 0 || got.Outcome != "complete" || got.DurableState != DurableFinal || !reflect.DeepEqual(got.Workload, final.Workload) || got.InstanceID != final.InstanceID || got.SSMCommandID != final.SSMCommandID || got.SubmittedAt != final.SubmittedAt || got.ExpiresAt != final.ExpiresAt || len(store.reads) != 1 || got.SSM != nil {
				t.Fatalf("historical result was not independently recoverable: %+v reads=%v", got, store.reads)
			}
			if !strings.Contains(got.Message, "not been downloaded or verified") {
				t.Fatalf("metadata retrieval claimed byte verification: %+v", got)
			}
		})
	}
}

func TestRecoverFinalIncompleteAndExpiredPreserveRemoteOutcome(t *testing.T) {
	for _, expired := range []bool{false, true} {
		final, _ := finalRecord()
		final.Publication, final.Streams.Stderr.Upload = "incomplete", "failed"
		final.Workload.ExitCode = aws.Int(4)
		expires, _ := execprotocol.ParseTimestamp(final.ExpiresAt)
		now := expires.Add(-time.Second)
		want := "incomplete"
		if expired {
			now, want = expires, "expired"
		}
		got := Recover(context.Background(), recoverMetadata(t, final, final), nil, final.Scope, final.CommandID, RecoverOptions{Now: func() time.Time { return now }})
		if got.Outcome != want || got.ExitCode != 1 || got.OK || !reflect.DeepEqual(got.Workload, final.Workload) || got.Publication != "incomplete" {
			t.Fatalf("lost independent workload/publication status: %+v", got)
		}
	}
}

func TestRecoverRejectsInvalidInputBeforeCloudReads(t *testing.T) {
	final, _ := finalRecord()
	for _, tc := range []struct {
		scope execprotocol.Scope
		id    string
	}{{final.Scope, "../stdout"}, {final.Scope, "DC1-0123456789abcdef0123456789abcdef"}, {execprotocol.Scope{}, final.CommandID}} {
		store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) {
			t.Fatal("invalid input reached cloud storage")
			return execprotocol.Object{}, nil
		}}
		got := Recover(context.Background(), store, nil, tc.scope, tc.id, RecoverOptions{})
		if got.Outcome != "invalid" || got.ExitCode != 2 || got.CommandID != "" {
			t.Fatalf("invalid input accepted: %+v", got)
		}
	}
}

func TestRecoverAbsentMetadataCannotProveExpiryOrSSMIdentity(t *testing.T) {
	final, _ := finalRecord()
	store := recoverMetadata(t, final)
	api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
		t.Fatal("SSM called without cloud-derived binding")
		return nil, nil
	})
	got := recoverResult(store, api, final)
	if got.Outcome != "missing_or_expired" || got.ExitCode != 1 || got.Workload != nil || got.ExpiresAt != "" || got.InstanceID != "" || got.SSMCommandID != "" || len(store.reads) != 6 {
		t.Fatalf("absence invented identity or expiry: %+v reads=%v", got, store.reads)
	}
}

func TestRecoverRequestRetentionUsesServerTimestamp(t *testing.T) {
	final, _ := finalRecord()
	request, _ := requestAndAck(final)
	request.RetentionDays = 2
	submitted, _ := execprotocol.ParseTimestamp(final.SubmittedAt)
	expires := submitted.Add(48 * time.Hour)
	for _, tc := range []struct {
		name string
		now  time.Time
		want string
	}{{"before deadline", expires.Add(-time.Second), "execution_unknown"}, {"at deadline", expires, "expired"}, {"after deadline", expires.Add(time.Second), "expired"}} {
		t.Run(tc.name, func(t *testing.T) {
			got := Recover(context.Background(), recoverMetadata(t, final, request), nil, final.Scope, final.CommandID, RecoverOptions{Now: func() time.Time { return tc.now }})
			if got.Outcome != tc.want || got.ExpiresAt != execprotocol.Timestamp(expires) || got.SubmittedAt != final.SubmittedAt || got.SSMCommandID != "" || got.Workload != nil || got.SubmissionState != "submission_unknown" {
				t.Fatalf("request retention not derived from cloud creation: %+v", got)
			}
		})
	}
	store := recoverMetadata(t, final, request)
	get := store.get
	store.get = func(ctx context.Context, key string) (execprotocol.Object, error) {
		object, err := get(ctx, key)
		object.LastModified = time.Time{}
		return object, err
	}
	got := recoverResult(store, nil, final)
	if got.Outcome != "corrupt" || got.ExpiresAt != "" {
		t.Fatalf("missing server timestamp fabricated expiry: %+v", got)
	}
}

func TestRecoverStartedAndOutcomeSnapshots(t *testing.T) {
	final, _ := finalRecord()
	started, outcome := intermediateRecords(final)
	request, ack := requestAndAck(final)
	for _, tc := range []struct {
		name    string
		records []execprotocol.Record
		want    string
		state   DurableState
	}{{"ack only", []execprotocol.Record{ack}, "execution_unknown", DurablePending}, {"started", []execprotocol.Record{request, ack, started}, "pending", DurableStarted}, {"outcome", []execprotocol.Record{request, ack, started, outcome}, "incomplete", DurableOutcome}} {
		t.Run(tc.name, func(t *testing.T) {
			store := recoverMetadata(t, final, tc.records...)
			got := recoverResult(store, nil, final)
			if got.Outcome != tc.want || got.DurableState != tc.state || got.ExitCode != 1 || got.SSMCommandID != final.SSMCommandID || got.InstanceID != final.InstanceID || len(store.reads) != 6 {
				t.Fatalf("wrong bounded snapshot: %+v reads=%v", got, store.reads)
			}
			if tc.state == DurableOutcome && (!reflect.DeepEqual(got.Workload, outcome.Workload) || got.StartedAt != started.StartedAt || got.Publication != "pending") {
				t.Fatalf("lost durable outcome: %+v", got)
			}
		})
	}
}

func TestRecoverStorageFailuresPreserveIndependentlyRecordedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		err        error
		data       []byte
	}{{"denied", "access_denied", execprotocol.ErrDenied, nil}, {"expired credentials", "unavailable", execprotocol.ErrCredentialsExpired, nil}, {"invalid credentials", "unavailable", execprotocol.ErrCredentialsInvalid, nil}, {"unavailable credentials", "unavailable", execprotocol.ErrCredentialsUnavailable, nil}, {"transport", "unavailable", execprotocol.ErrUnavailable, nil}, {"corrupt", "corrupt", nil, []byte(`{"schema_version":1,"SECRET":true}`)}, {"unsupported", "unsupported_schema", nil, []byte(`{"schema_version":2}`)}} {
		t.Run(tc.name, func(t *testing.T) {
			final, _ := finalRecord()
			_, outcome := intermediateRecords(final)
			outcome.Workload.ExitCode = aws.Int(2)
			store := recoverMetadata(t, final, outcome)
			get := store.get
			store.get = func(ctx context.Context, key string) (execprotocol.Object, error) {
				if strings.HasSuffix(key, "/result.json") {
					if tc.data != nil {
						return finalObject(tc.data), nil
					}
					return execprotocol.Object{}, tc.err
				}
				return get(ctx, key)
			}
			got := recoverResult(store, nil, final)
			if got.Outcome != tc.want || got.ExitCode != 1 || !reflect.DeepEqual(got.Workload, outcome.Workload) || got.DurableState != DurableOutcome || got.Publication != "pending" {
				t.Fatalf("metadata failure hid independent workload: %+v", got)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestRecoverReconcilesEveryObservedBindingAndTimeline(t *testing.T) {
	for _, field := range []string{"scope", "command", "instance", "document", "runner", "payload", "ssm", "deadline", "start", "request retention", "request timestamp"} {
		t.Run(field, func(t *testing.T) {
			final, _ := finalRecord()
			started, outcome := intermediateRecords(final)
			request, ack := requestAndAck(final)
			switch field {
			case "scope":
				started.Scope.Owner = "different"
			case "command":
				started.CommandID = "dc1-ffffffffffffffffffffffffffffffff"
			case "instance":
				started.InstanceID = "i-fffffffffffffffff"
			case "document":
				started.Document.Version = "2"
			case "runner":
				started.RunnerSHA256 = strings.Repeat("f", 64)
			case "payload":
				started.PayloadSHA256 = strings.Repeat("f", 64)
			case "ssm":
				ack.SSMCommandID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
			case "deadline":
				expires, _ := execprotocol.ParseTimestamp(started.ExpiresAt)
				started.ExpiresAt = execprotocol.Timestamp(expires.Add(24 * time.Hour))
			case "start":
				finished, _ := execprotocol.ParseTimestamp(outcome.FinishedAt)
				started.StartedAt = execprotocol.Timestamp(finished.Add(time.Second))
			case "request retention":
				request.RetentionDays = 31
			}
			store := recoverMetadata(t, final, outcome, started, request, ack)
			if field == "request timestamp" {
				get := store.get
				store.get = func(ctx context.Context, key string) (execprotocol.Object, error) {
					object, err := get(ctx, key)
					object.LastModified = object.LastModified.Add(time.Second)
					return object, err
				}
			}
			got := recoverResult(store, nil, final)
			if got.Outcome != "corrupt" || got.ExitCode != 1 || !reflect.DeepEqual(got.Workload, outcome.Workload) || got.SSMCommandID != final.SSMCommandID {
				t.Fatalf("accepted contradictory %s metadata: %+v", field, got)
			}
		})
	}
}

func TestRecoverSSMSnapshotNeverInventsWorkloadExit(t *testing.T) {
	for _, tc := range []struct {
		status, details, want string
		state                 InvocationState
		code                  int32
	}{{"Pending", "Pending", "pending", InvocationPending, -1}, {"InProgress", "In Progress", "pending", InvocationRunning, -1}, {"Delayed", "Delayed", "pending", InvocationDelayed, -1}, {"Cancelling", "Cancelling", "pending", InvocationCancellationPending, -1}, {"Success", "Success", "execution_unknown", InvocationSucceeded, 0}, {"Failed", "Failed", "execution_unknown", InvocationFailed, 255}, {"TimedOut", "Execution Timed Out", "execution_unknown", InvocationRunnerTimeout, 137}, {"TimedOut", "Delivery Timed Out", "execution_unknown", InvocationDeliveryTimeout, -1}, {"SECRET status", "SECRET detail", "execution_unknown", InvocationUnknown, -1}} {
		t.Run(tc.details, func(t *testing.T) {
			final, sub := finalRecord()
			_, ack := requestAndAck(final)
			calls := 0
			api := invocationFunc(func(_ context.Context, input *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				calls++
				if aws.ToString(input.CommandId) != ack.SSMCommandID || aws.ToString(input.InstanceId) != ack.InstanceID || aws.ToString(input.PluginName) != ack.Document.Step {
					t.Fatalf("lookup ignored cloud-derived binding: %+v", input)
				}
				return invocation(sub, tc.status, tc.details, tc.code), nil
			})
			got := recoverResult(recoverMetadata(t, final, ack), api, final)
			if got.Outcome != tc.want || got.ExitCode != 1 || got.Workload != nil || got.SSM == nil || got.SSM.State != tc.state || *got.SSM.ResponseCode != tc.code || calls != 1 {
				t.Fatalf("wrapper became workload or retrieval success: %+v calls=%d", got, calls)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestRecoverOptionalSSMFailuresAndPublicationRace(t *testing.T) {
	for _, apiErr := range []error{nil, &ssmtypes.InvocationDoesNotExist{}, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "SECRET"}, errors.New("SECRET transport error")} {
		for _, publish := range []bool{false, true} {
			final, sub := finalRecord()
			_, outcome := intermediateRecords(final)
			store := recoverMetadata(t, final, outcome)
			calls := 0
			get := store.get
			store.get = func(ctx context.Context, key string) (execprotocol.Object, error) {
				if publish && calls > 0 && strings.HasSuffix(key, "/result.json") {
					return finalObject(encodedFinal(t, final)), nil
				}
				return get(ctx, key)
			}
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				calls++
				if apiErr != nil {
					return nil, apiErr
				}
				return invocation(sub, "InProgress", "In Progress", -1), nil
			})
			got := recoverResult(store, api, final)
			want, exit := "incomplete", 1
			if publish {
				want, exit = "complete", 0
			}
			if got.Outcome != want || got.ExitCode != exit || calls != 1 || !reflect.DeepEqual(got.Workload, outcome.Workload) {
				t.Fatalf("optional SSM hid durable evidence: %+v calls=%d", got, calls)
			}
			assertNoUntrustedContent(t, got)
		}
	}
}

func TestRecoverSSMFailureWithoutOutcomeRemainsAnIndependentObservation(t *testing.T) {
	for _, tc := range []struct {
		name, want, code string
		err              error
	}{{"missing history", "execution_unknown", "execution_unknown", &ssmtypes.InvocationDoesNotExist{}}, {"denied", "access_denied", "ssm_access_denied", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "SECRET"}}, {"transport", "unavailable", "ssm_unavailable", errors.New("SECRET transport error")}} {
		t.Run(tc.name, func(t *testing.T) {
			final, _ := finalRecord()
			_, ack := requestAndAck(final)
			calls := 0
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				calls++
				return nil, tc.err
			})
			got := recoverResult(recoverMetadata(t, final, ack), api, final)
			if got.Outcome != tc.want || got.Code != tc.code || got.ExitCode != 1 || got.Workload != nil || got.SSM != nil || got.SSMCommandID != final.SSMCommandID || calls != 1 {
				t.Fatalf("optional SSM failure invented a durable result: %+v calls=%d", got, calls)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}

func TestRecoverCompleteFinalSupersedesOptionalReadFailureButNotCorruption(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		final, _ := finalRecord()
		_, outcome := intermediateRecords(final)
		store := recoverMetadata(t, final, outcome)
		get := store.get
		finalReads := 0
		store.get = func(ctx context.Context, key string) (execprotocol.Object, error) {
			if strings.HasSuffix(key, "/result.json") {
				finalReads++
				if finalReads == 1 {
					return execprotocol.Object{}, execprotocol.ErrDenied
				}
				return finalObject(encodedFinal(t, final)), nil
			}
			if strings.HasSuffix(key, "/request.json") {
				if corrupt {
					return finalObject([]byte(`{"schema_version":1,"SECRET":true}`)), nil
				}
				return execprotocol.Object{}, execprotocol.ErrDenied
			}
			return get(ctx, key)
		}
		got := recoverResult(store, nil, final)
		want, exit, reads := "complete", 0, 2
		if corrupt {
			want, exit, reads = "corrupt", 1, 1
		}
		if got.Outcome != want || got.ExitCode != exit || finalReads != reads || !reflect.DeepEqual(got.Workload, outcome.Workload) {
			t.Fatalf("final race lost failure precedence: %+v final_reads=%d", got, finalReads)
		}
		if !corrupt && (len(got.Warnings) != 1 || got.Warnings[0] != "access_denied") {
			t.Fatalf("optional denied read was not retained as a warning: %+v", got)
		}
		assertNoUntrustedContent(t, got)
	}
}

func TestRecoverPublicationRaceRejectsContradictions(t *testing.T) {
	for _, change := range []string{"workload", "stream", "capture", "retention", "binding", "ssm identity"} {
		t.Run(change, func(t *testing.T) {
			final, sub := finalRecord()
			_, outcome := intermediateRecords(final)
			store := recoverMetadata(t, final, outcome)
			calls := 0
			get := store.get
			store.get = func(ctx context.Context, key string) (execprotocol.Object, error) {
				if calls > 0 && strings.HasSuffix(key, "/result.json") {
					return finalObject(encodedFinal(t, final)), nil
				}
				return get(ctx, key)
			}
			switch change {
			case "workload":
				final.Workload = &execprotocol.Workload{Status: "exited", ExitCode: aws.Int(4)}
			case "stream":
				final.Streams.Stdout.Bytes, final.Streams.Stdout.SHA256 = 1, execprotocol.Digest([]byte("x"))
			case "capture":
				final.Capture, final.Publication = "incomplete", "incomplete"
			case "retention":
				expires, _ := execprotocol.ParseTimestamp(final.ExpiresAt)
				final.ExpiresAt = execprotocol.Timestamp(expires.Add(24 * time.Hour))
			case "binding":
				final.RunnerSHA256 = strings.Repeat("f", 64)
			}
			api := invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				calls++
				out := invocation(sub, "Success", "Success", 0)
				if change == "ssm identity" {
					out.DocumentVersion = aws.String("99")
				}
				return out, nil
			})
			got := recoverResult(store, api, outcome)
			if got.Outcome != "corrupt" || got.ExitCode != 1 || !reflect.DeepEqual(got.Workload, outcome.Workload) || got.DurableState != DurableOutcome {
				t.Fatalf("completion race overrode contradictory evidence: %+v", got)
			}
		})
	}
}

func TestRecoverBoundsTransientReadsAndStopsUnderCallerDeadline(t *testing.T) {
	final, _ := finalRecord()
	store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) {
		return execprotocol.Object{}, execprotocol.ErrUnavailable
	}}
	got := recoverResult(store, nil, final)
	if got.Outcome != "unavailable" || len(store.reads) != 12 {
		t.Fatalf("transient snapshot was not bounded to two attempts per read: %+v calls=%d", got, len(store.reads))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	store = &finalStore{get: func(ctx context.Context, _ string) (execprotocol.Object, error) {
		<-ctx.Done()
		return execprotocol.Object{}, ctx.Err()
	}}
	got = Recover(ctx, store, nil, final.Scope, final.CommandID, RecoverOptions{})
	if got.Outcome != "timeout" || got.ExitCode != 4 || len(store.reads) != 1 {
		t.Fatalf("deadline caused retries or fabricated workload: %+v reads=%v", got, store.reads)
	}
}

func TestRecoverCancellationPreservesOutcomeAndEstablishedFinal(t *testing.T) {
	for _, complete := range []bool{false, true} {
		final, _ := finalRecord()
		_, outcome := intermediateRecords(final)
		ctx, cancel := context.WithCancel(context.Background())
		store := &finalStore{get: func(_ context.Context, key string) (execprotocol.Object, error) {
			if complete && strings.HasSuffix(key, "/result.json") {
				cancel()
				return finalObject(encodedFinal(t, final)), nil
			}
			if strings.HasSuffix(key, "/outcome.json") {
				cancel()
				return finalObject(encodedFinal(t, outcome)), nil
			}
			return execprotocol.Object{}, execprotocol.ErrNotFound
		}}
		got := Recover(ctx, store, nil, final.Scope, final.CommandID, RecoverOptions{})
		cancel()
		want, exit := "interrupted", 4
		if complete {
			want, exit = "complete", 0
		}
		if got.Outcome != want || got.ExitCode != exit || !reflect.DeepEqual(got.Workload, final.Workload) {
			t.Fatalf("cancellation discarded established evidence: %+v", got)
		}
	}
}
