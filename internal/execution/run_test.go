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
