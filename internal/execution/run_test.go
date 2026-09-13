package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

func TestRunRejectsInputsBeforeAWS(t *testing.T) {
	for _, o := range []RunOptions{
		{Target: "bad target", Argv: []string{"true"}},
		{Target: "smoke"}, {Target: "smoke", Argv: []string{""}},
		{Target: "smoke", Argv: []string{"echo", string([]byte{0xff})}},
		{Target: "smoke", Argv: []string{"echo", "a\x00b"}},
		{Target: "smoke", Argv: []string{"true"}, Cwd: string([]byte{0xff})},
		{Target: "smoke", Argv: []string{"true"}, Cwd: "/a\x00b"},
		{Target: "smoke", Argv: []string{"true"}, ExecTimeout: 500 * time.Millisecond},
		{Target: "smoke", Argv: []string{"true"}, WaitTimeout: 26 * time.Hour},
	} {
		r := Run(context.Background(), filepath.Join(t.TempDir(), "not-read.toml"), config.Overrides{}, o, Dependencies{New: func(context.Context, config.Config, config.Manifest) (*Service, error) {
			t.Fatal("invalid arguments reached AWS")
			return nil, nil
		}}, io.Discard)
		if r.ExitCode != 2 || r.Outcome != "config_invalid" || r.CommandID != "" {
			t.Fatalf("invalid result: %+v", r)
		}
	}
}

func TestRunIdentityFailuresAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		err     error
		outcome string
		exit    int
	}{
		{errors.New("SECRET SDK failure"), "setup_failed", 1},
		{&identity.Failure{Code: "account_mismatch", Message: "select the intended profile"}, "setup_failed", 1},
		{&identity.Failure{Code: "timeout", Message: "identity deadline expired"}, "setup_timeout", 4},
	} {
		r := Run(context.Background(), testutil.Setup(t), config.Overrides{}, RunOptions{Target: "smoke", Argv: []string{"true"}}, Dependencies{New: func(context.Context, config.Config, config.Manifest) (*Service, error) { return nil, tc.err }}, io.Discard)
		data, _ := json.Marshal(r)
		if r.Outcome != tc.outcome || r.ExitCode != tc.exit || strings.Contains(string(data), "SECRET") {
			t.Fatalf("unsafe result: %s", data)
		}
	}
}

func TestRunDispatchAndIndependentFinalWait(t *testing.T) {
	for _, failAck := range []bool{false, true} {
		path := testutil.Setup(t)
		// These files intentionally do not exist: neither is an exec prerequisite.
		testutil.Write(t, path, testutil.Config+"profile_file='missing-profile.toml'\nssh_identity_file='missing-key'\n")
		service, api, store, _, _ := dispatchSetup(t)
		if failAck {
			store.ackErr = execprotocol.ErrDenied
		}
		api.sendActual = func(_ context.Context, in *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
			id := in.Parameters["requestId"][0]
			key := execprotocol.ObjectKey(execprotocol.Scope{Account: service.Scope.ExpectedAccount, Region: service.Scope.Region, Deployment: service.Scope.Deployment, Owner: service.Scope.Owner}, id, "request.json")
			request, err := execprotocol.DecodeRecord(store.values[key])
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			code := 4
			stream := func(name string) execprotocol.Stream {
				return execprotocol.Stream{Key: execprotocol.ObjectKey(request.Scope, id, name), Bytes: 0, SHA256: execprotocol.Digest(nil), Upload: "complete"}
			}
			final := execprotocol.Record{SchemaVersion: 1, Kind: "result", Binding: request.Binding, SSMCommandID: acknowledgedID, SubmittedAt: execprotocol.Timestamp(now), ExpiresAt: execprotocol.Timestamp(now.Add(30 * 24 * time.Hour)), FinishedAt: execprotocol.Timestamp(now), FinalizedAt: execprotocol.Timestamp(now), Workload: &execprotocol.Workload{Status: "exited", ExitCode: &code}, Capture: "complete", Publication: "complete", Streams: &execprotocol.Streams{Stdout: stream("stdout"), Stderr: stream("stderr")}}
			data, err := execprotocol.EncodeRecord(final)
			if err != nil {
				t.Fatal(err)
			}
			store.values[execprotocol.ObjectKey(request.Scope, id, "result.json")] = data
			started, outcome := intermediateRecords(final)
			store.values[execprotocol.ObjectKey(request.Scope, id, "started.json")] = encodedFinal(t, started)
			store.values[execprotocol.ObjectKey(request.Scope, id, "outcome.json")] = encodedFinal(t, outcome)
			return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(acknowledgedID)}}, nil
		}
		var setupContext context.Context
		var diag bytes.Buffer
		r := Run(context.Background(), path, config.Overrides{AWSProfile: "selected-profile", Region: "us-east-2"}, RunOptions{Target: "smoke", Argv: []string{"false"}, WaitTimeout: time.Second}, Dependencies{New: func(ctx context.Context, c config.Config, m config.Manifest) (*Service, error) {
			setupContext = ctx
			if c.AWSProfile != "selected-profile" || m.SchemaVersion != 4 {
				t.Fatal("scope not forwarded")
			}
			return service, nil
		}}, &diag)
		if r.Outcome != "remote_exit" || r.ExitCode != 4 || r.OK || r.Workload == nil || *r.Workload.ExitCode != 4 || r.SubmissionState != "submitted" || len(api.sent) != 1 {
			t.Fatalf("wrong completed result: %+v", r)
		}
		if setupContext.Err() != context.Canceled {
			t.Fatal("setup context was not released before independent wait")
		}
		if len(r.Warnings) != map[bool]int{false: 0, true: 1}[failAck] {
			t.Fatal("acknowledgement diagnostic missing")
		}
		if !strings.Contains(r.RecoveryCommand, "selected-profile") || !strings.Contains(r.RecoveryCommand, path) || !strings.HasSuffix(r.RecoveryCommand, " logs "+r.CommandID) || !strings.Contains(diag.String(), "submitted command_id="+r.CommandID) {
			t.Fatal("recovery scope or early ID lost")
		}
	}
}

func TestRunUnknownSubmissionAndInterruptionRetainIdentity(t *testing.T) {
	for _, cancelDuringSend := range []bool{false, true} {
		service, api, _, _, _ := dispatchSetup(t)
		ctx, cancel := context.WithCancel(context.Background())
		api.sendActual = func(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
			if cancelDuringSend {
				cancel()
			}
			return nil, errors.New("SECRET accepted then disconnected")
		}
		r := Run(ctx, testutil.Setup(t), config.Overrides{}, RunOptions{Target: "smoke", Argv: []string{"true"}}, Dependencies{New: func(context.Context, config.Config, config.Manifest) (*Service, error) { return service, nil }}, io.Discard)
		cancel()
		wantOutcome, wantExit := "submission_unknown", 1
		if cancelDuringSend {
			wantOutcome, wantExit = "interrupted", 4
		}
		data, _ := json.Marshal(r)
		if r.Outcome != wantOutcome || r.ExitCode != wantExit || r.SubmissionState != "submission_unknown" || !execprotocol.ValidCommandID(r.CommandID) || r.SSMCommandID != "" || r.RecoveryCommand == "" || len(api.sent) != 1 || strings.Contains(string(data), "SECRET") {
			t.Fatalf("lost uncertain submission: %s", data)
		}
	}
}

func TestRunSetupDeadlineDoesNotBecomeRemoteTimeout(t *testing.T) {
	service, api, store, _, _ := dispatchSetup(t)
	service.Targets = dispatchTargets{resolve: func(ctx context.Context, _ string) ([]lifecycle.Instance, error) { <-ctx.Done(); return nil, ctx.Err() }}
	r := Run(context.Background(), testutil.Setup(t), config.Overrides{}, RunOptions{Target: "smoke", Argv: []string{"true"}, SetupTimeout: time.Millisecond}, Dependencies{New: func(context.Context, config.Config, config.Manifest) (*Service, error) { return service, nil }}, io.Discard)
	if r.Outcome != "setup_timeout" || r.ExitCode != 4 || r.Workload != nil || len(api.sent) != 0 || store.puts != 0 {
		t.Fatalf("local deadline confused with remote execution: %+v", r)
	}
}

type observingDispatchSSM struct {
	*dispatchSSM
	observe invocationFunc
}

func (s observingDispatchSSM) GetCommandInvocation(ctx context.Context, input *ssm.GetCommandInvocationInput, options ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	return s.observe(ctx, input, options...)
}

type observingDispatchStore struct{ *dispatchStore }

func (s observingDispatchStore) Get(ctx context.Context, key string) (execprotocol.Object, error) {
	if _, exists := s.values[key]; !exists {
		return execprotocol.Object{}, execprotocol.ErrNotFound
	}
	return s.dispatchStore.Get(ctx, key)
}

func TestRunPostDispatchObservationFailuresRetainRecoveryAndStatus(t *testing.T) {
	for _, scenario := range []struct {
		name, outcome, code string
		exit                int
	}{
		{"interrupt", "interrupted", "interrupted", 4},
		{"wait deadline", "observation_timeout", "observation_timeout", 4},
		{"permission denied", "api_failed", "ssm_access_denied", 1},
		{"credentials expired", "api_failed", "credentials_expired", 1},
		{"runner timeout with known exit", "result_incomplete", "result_incomplete", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, api, store, _, _ := dispatchSetup(t)
			service.Store = observingDispatchStore{store}
			store.ackErr = execprotocol.ErrDenied
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var submitted Submission
			api.sendActual = func(_ context.Context, input *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
				id := input.Parameters["requestId"][0]
				scope := execprotocol.Scope{Account: service.Scope.ExpectedAccount, Region: service.Scope.Region, Deployment: service.Scope.Deployment, Owner: service.Scope.Owner}
				request, err := execprotocol.DecodeRecord(store.values[execprotocol.ObjectKey(scope, id, "request.json")])
				if err != nil {
					t.Fatal(err)
				}
				submitted = Submission{Binding: request.Binding, SSMCommandID: acknowledgedID}
				final, _ := finalRecord()
				final.Binding, final.SSMCommandID = request.Binding, acknowledgedID
				final.Workload.ExitCode = aws.Int(4)
				final.Streams.Stdout.Key = execprotocol.ObjectKey(scope, id, "stdout")
				final.Streams.Stderr.Key = execprotocol.ObjectKey(scope, id, "stderr")
				started, outcome := intermediateRecords(final)
				store.values[execprotocol.ObjectKey(scope, id, "started.json")] = encodedFinal(t, started)
				store.values[execprotocol.ObjectKey(scope, id, "outcome.json")] = encodedFinal(t, outcome)
				return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(acknowledgedID)}}, nil
			}
			observations := 0
			service.SSM = observingDispatchSSM{dispatchSSM: api, observe: func(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
				observations++
				switch scenario.name {
				case "interrupt":
					cancel()
				case "permission denied":
					return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "SECRET raw SDK error"}
				case "credentials expired":
					return nil, &smithy.GenericAPIError{Code: "ExpiredTokenException", Message: "SECRET raw SDK error"}
				case "runner timeout with known exit":
					return invocation(submitted, "TimedOut", "Execution Timed Out", 137), nil
				}
				return invocation(submitted, "InProgress", "In Progress", -1), nil
			}}
			path := testutil.Setup(t)
			var diagnostics bytes.Buffer
			got := Run(ctx, path, config.Overrides{AWSProfile: "selected-profile"}, RunOptions{Target: "smoke", Argv: []string{"true"}, WaitTimeout: 10 * time.Millisecond}, Dependencies{New: func(context.Context, config.Config, config.Manifest) (*Service, error) { return service, nil }}, &diagnostics)
			assertFinalIdentity(t, got, submitted)
			if got.Outcome != scenario.outcome || got.Code != scenario.code || got.ExitCode != scenario.exit || got.SubmissionState != "submitted" || got.Workload == nil || *got.Workload.ExitCode != 4 || got.DurableState != DurableOutcome || len(api.sent) != 1 || observations != 1 || !strings.Contains(got.RecoveryCommand, "selected-profile") || !strings.Contains(got.RecoveryCommand, path) || !strings.HasSuffix(got.RecoveryCommand, " logs "+got.CommandID) {
				t.Fatalf("post-dispatch observation lost identity or trusted workload: %+v sends=%d observations=%d", got, len(api.sent), observations)
			}
			foundWarning := false
			for _, code := range got.Warnings {
				foundWarning = foundWarning || code == "acknowledgement_publication_failed"
			}
			if !foundWarning || !strings.Contains(diagnostics.String(), "submitted command_id="+got.CommandID+" ssm_command_id="+got.SSMCommandID) {
				t.Fatalf("acknowledged IDs or acknowledgement warning lost: %+v", got)
			}
			assertNoUntrustedContent(t, got)
		})
	}
}
