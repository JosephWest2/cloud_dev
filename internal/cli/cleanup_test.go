package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/JosephWest2/cloud_dev/internal/expirycleanup"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

var cleanupEpoch = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
var cleanupScope = expiry.Scope{Account: "123456789012", Region: "us-east-2", Deployment: "dev", Owner: "joe"}

type cleanupClock struct{}

func (cleanupClock) Now() time.Time { return cleanupEpoch }

type cleanupSTS string

func (s cleanupSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: aws.String(string(s))}, nil
}

type cleanupFakeAPI struct {
	mu            sync.Mutex
	rows          []types.Instance
	writes        int
	reads         int
	volumeReads   int
	deny          string
	uncertainRoot bool
	cancel        context.CancelFunc
}

func (*cleanupFakeAPI) Options() ec2.Options { return ec2.Options{Region: cleanupScope.Region} }
func (a *cleanupFakeAPI) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reads++
	var rows []types.Instance
	for _, r := range a.rows {
		if len(in.InstanceIds) == 0 || in.InstanceIds[0] == aws.ToString(r.InstanceId) {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(cleanupScope.Account), Instances: rows}}}, nil
}
func (a *cleanupFakeAPI) TerminateInstances(ctx context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes++
	id := in.InstanceIds[0]
	if id == a.deny {
		return nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "SECRET provider error"}
	}
	for n, r := range a.rows {
		if aws.ToString(r.InstanceId) == id {
			a.rows[n].State = &types.InstanceState{Name: "terminated"}
		}
	}
	if a.cancel != nil {
		a.cancel()
	}
	return &ec2.TerminateInstancesOutput{TerminatingInstances: []types.InstanceStateChange{{InstanceId: aws.String(id), CurrentState: &types.InstanceState{Name: "shutting-down"}}}}, nil
}
func (a *cleanupFakeAPI) DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.volumeReads++
	if a.uncertainRoot {
		return &ec2.DescribeVolumesOutput{}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound", Message: "SECRET"}
}
func cleanupRow(n int, expires string) types.Instance {
	return types.Instance{InstanceId: aws.String(fmt.Sprintf("i-%08x", n)), State: &types.InstanceState{Name: "running"}, RootDeviceName: aws.String("/dev/xvda"), RootDeviceType: "ebs", Placement: &types.Placement{AvailabilityZone: aws.String("us-east-2a")}, BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/xvda"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String(fmt.Sprintf("vol-%08x", n)), DeleteOnTermination: aws.Bool(true)}}}, Tags: []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String("dev")}, {Key: aws.String("Owner"), Value: aws.String("joe")}, {Key: aws.String("ExpiresAt"), Value: aws.String(expires)}}}
}
func cleanupConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`schema_version=1
expected_account="123456789012"
region="us-east-2"
deployment="dev"
owner="joe"
aws_profile="configured"
manifest="missing.json"
profile_file="missing.toml"
ssh_identity_file="missing-key"
max_count=-1
`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func cleanupDeps(t *testing.T, a *cleanupFakeAPI, account string) cleanupDependencies {
	t.Helper()
	return cleanupDependencies{Clock: cleanupClock{}, LoadAWS: func(ctx context.Context, c config.Config) (aws.Config, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > cleanupTimeout {
			t.Error("unbounded credential load")
		}
		if c.ExpectedAccount != cleanupScope.Account || c.Owner != cleanupScope.Owner || c.Deployment != cleanupScope.Deployment || c.Region != cleanupScope.Region {
			t.Errorf("wrong config: %+v", c)
		}
		return aws.Config{Region: c.Region}, nil
	}, New: func(scope expiry.Scope, ac aws.Config, clock expiry.Clock, sink expiry.Sink, _ expirycleanup.Limits) (cleanupService, error) {
		if scope != cleanupScope || ac.Region != scope.Region {
			t.Errorf("scope changed: %+v", scope)
		}
		return expirycleanup.New(scope, expirycleanup.Dependencies{EC2: a, STS: cleanupSTS(account), Clock: clock, Sink: sink, Wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}, expirycleanup.Limits{ObservationAttempts: 1})
	}}
}
func cleanupDecode(t *testing.T, data []byte) expiry.Result {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(data))
	var r expiry.Result
	if err := d.Decode(&r); err != nil {
		t.Fatalf("invalid result %q: %v", data, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		t.Fatal("extra envelope", err)
	}
	if r.SchemaVersion != 1 || r.Command != "cleanup" || r.Instances == nil || r.Errors == nil {
		t.Fatalf("bad envelope: %+v", r)
	}
	return r
}
func TestCleanupAdapterSharedFixtures(t *testing.T) {
	for _, mode := range []string{"empty", "dry", "malformed", "mixed", "root-unknown", "account-mismatch", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a := &cleanupFakeAPI{rows: []types.Instance{cleanupRow(1, cleanupEpoch.Format(time.RFC3339Nano))}}
			account := cleanupScope.Account
			dry := mode == "dry"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "empty":
				a.rows = nil
			case "malformed":
				a.rows[0].Tags[3].Value = aws.String("SECRET invalid")
			case "mixed":
				a.rows = append(a.rows, cleanupRow(2, cleanupEpoch.Format(time.RFC3339Nano)))
				a.deny = "i-00000002"
			case "root-unknown":
				a.uncertainRoot = true
			case "account-mismatch":
				account = "999999999999"
			case "cancel":
				a.cancel = cancel
			}
			var stdout, stderr bytes.Buffer
			deps := cleanupDeps(t, a, account)
			r := runCleanup(ctx, cleanupConfig(t), config.Overrides{}, dry, &stderr, deps)
			code := emitCleanup(r, true, &stdout, &stderr)
			got := cleanupDecode(t, stdout.Bytes())
			if code != got.ExitCode || got.Scope != cleanupScope || strings.Contains(stdout.String()+stderr.String(), "SECRET") {
				t.Fatalf("%+v stderr=%s", got, stderr.String())
			}
			switch mode {
			case "empty":
				if got.Code != "cleanup_no_candidates" || code != 0 {
					t.Fatal(got)
				}
			case "dry":
				if a.writes != 0 || a.volumeReads != 0 || a.reads != 1 || stderr.Len() != 0 || got.Instances[0].Status != "would_terminate" {
					t.Fatalf("%+v writes=%d", got, a.writes)
				}
			case "malformed":
				if code != 1 || a.writes != 0 || got.Instances[0].Reason != expiry.ExpiryInvalid {
					t.Fatal(got)
				}
			case "mixed":
				if code != 3 || got.CleanedCount != 1 || len(got.Instances) != 2 {
					t.Fatal(got)
				}
			case "root-unknown":
				if code != 1 || got.CleanedCount != 0 || got.TerminatedCount != 1 {
					t.Fatal(got)
				}
			case "account-mismatch":
				if code != 1 || a.reads != 0 || a.writes != 0 {
					t.Fatal(got)
				}
			case "cancel":
				if code != 4 || len(got.Instances) != 1 || len(got.Instances[0].Volumes) != 1 {
					t.Fatal(got)
				}
			}
		})
	}
}
func TestCleanupAdapterDecisionsEqualDirectService(t *testing.T) {
	path := cleanupConfig(t)
	legacy := cleanupRow(3, "")
	legacy.Tags = legacy.Tags[:3]
	rows := []types.Instance{cleanupRow(1, cleanupEpoch.Format(time.RFC3339Nano)), cleanupRow(2, cleanupEpoch.Add(time.Hour).Format(time.RFC3339Nano)), legacy}
	a := &cleanupFakeAPI{rows: rows}
	deps := cleanupDeps(t, a, cleanupScope.Account)
	viaCLI := runCleanup(context.Background(), path, config.Overrides{}, true, io.Discard, deps)
	direct, err := expirycleanup.New(cleanupScope, expirycleanup.Dependencies{EC2: &cleanupFakeAPI{rows: rows}, STS: cleanupSTS(cleanupScope.Account), Clock: cleanupClock{}, Sink: &cleanupSink{newCleanupOutput(io.Discard)}}, expirycleanup.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := direct.Run(context.Background(), true)
	if !reflect.DeepEqual(viaCLI, shared) {
		t.Fatalf("adapter drift:\n%+v\n%+v", viaCLI, shared)
	}
}
func TestCleanupCredentialsScopeAndEarlyDeadline(t *testing.T) {
	path := cleanupConfig(t)
	calls := 0
	deps := cleanupDependencies{LoadAWS: func(ctx context.Context, c config.Config) (aws.Config, error) {
		calls++
		if c.AWSProfile != "explicit" || c.Region != "us-west-2" {
			t.Fatalf("%+v", c)
		}
		return aws.Config{}, errors.New("SECRET")
	}}
	r := runCleanup(context.Background(), path, config.Overrides{AWSProfile: "explicit", Region: "us-west-2"}, true, io.Discard, deps)
	if calls != 1 || r.ExitCode != 1 || strings.Contains(r.Message, "SECRET") || r.Scope.Region != "us-west-2" {
		t.Fatal(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = runCleanup(ctx, path, config.Overrides{}, false, io.Discard, deps)
	if calls != 1 || r.ExitCode != 4 {
		t.Fatal(r)
	}
}

type cleanupBrokenWriter struct{}

func (cleanupBrokenWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

type cleanupShortWriter struct{}

func (cleanupShortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }
func TestCleanupOutputFailureRetainsEnvelope(t *testing.T) {
	for _, writer := range []io.Writer{cleanupBrokenWriter{}, cleanupShortWriter{}} {
		a := &cleanupFakeAPI{rows: []types.Instance{cleanupRow(1, cleanupEpoch.Format(time.RFC3339Nano))}}
		r := runCleanup(context.Background(), cleanupConfig(t), config.Overrides{}, true, io.Discard, cleanupDeps(t, a, cleanupScope.Account))
		var stderr bytes.Buffer
		if code := emitCleanup(r, true, writer, &stderr); code != 1 {
			t.Fatal(code)
		}
		_, body, ok := strings.Cut(stderr.String(), "\n")
		if !ok {
			t.Fatal(stderr.String())
		}
		got := cleanupDecode(t, []byte(body))
		if len(got.Instances) != 1 || got.Instances[0].Volumes[0].ID != "vol-00000001" {
			t.Fatal(got)
		}
	}
}
func TestCleanupEvidenceFailurePreventsMutation(t *testing.T) {
	a := &cleanupFakeAPI{rows: []types.Instance{cleanupRow(1, cleanupEpoch.Format(time.RFC3339Nano))}}
	r := runCleanup(context.Background(), cleanupConfig(t), config.Overrides{}, false, cleanupShortWriter{}, cleanupDeps(t, a, cleanupScope.Account))
	if r.OK || a.writes != 0 || len(r.Instances) != 1 {
		t.Fatalf("%+v writes=%d", r, a.writes)
	}
}

type cleanupBlockingWriter struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (w *cleanupBlockingWriter) Write(p []byte) (int, error) {
	w.calls.Add(1)
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return len(p), nil
}
func TestCleanupSinkBoundedAndConcurrent(t *testing.T) {
	blocked := &cleanupBlockingWriter{started: make(chan struct{}, 1), release: make(chan struct{})}
	output := newCleanupOutput(blocked)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := output.write(ctx, []byte("event\n")); err == nil {
				t.Error("acknowledged blocked write")
			}
		}()
	}
	wg.Wait()
	if blocked.calls.Load() != 1 {
		t.Fatal("unbounded writer goroutines", blocked.calls.Load())
	}
	close(blocked.release)
	// A normal buffer is safe despite concurrent service event delivery.
	var buf bytes.Buffer
	sink := &cleanupSink{newCleanupOutput(&buf)}
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sink.Emit(context.Background(), expiry.Event{SchemaVersion: 1, Kind: "decision", Scope: cleanupScope}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	decoder := json.NewDecoder(&buf)
	count := 0
	for {
		var e expiry.Event
		if err := decoder.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 20 {
		t.Fatal(count)
	}
}

func TestCleanupSyntaxRejectsBeforeFactory(t *testing.T) {
	tests := [][]string{
		{"cleanup", "target"}, {"cleanup", "--all"}, {"cleanup", "--group", "x"}, {"cleanup", "--yes"}, {"cleanup", "--ttl", "2h"}, {"cleanup", "--on-demand"}, {"cleanup", "--count", "1"}, {"cleanup", "--resume", "01234567890123456789012345678901"}, {"cleanup", "--dry-run", "--dry-run"}, {"cleanup", "--json", "--json"}, {"cleanup", "--timeout", "1s", "--timeout", "2s"}, {"cleanup", "--config", "a", "--config", "b"}, {"cleanup", "--dry-run=true"}, {"cleanup", "--timeout", "0"}, {"cleanup", "--timeout", "6m"}, {"cleanup", "--timeout", "bad"}, {"cleanup", "--"}, {"--timeout", "bad", "cleanup"}, {"--unknown", "cleanup"}, {"cleanup", "--aws-profile="}, {"cleanup", "--region"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			args = append(append([]string(nil), args...), "--json")
			var stdout, stderr bytes.Buffer
			calls := 0
			runner := func(context.Context, string, config.Overrides, bool, io.Writer) expiry.Result {
				calls++
				return expiry.Result{}
			}
			if !isCleanupCommand(args) {
				t.Fatal("cleanup not recognized")
			}
			code := runCleanupCommand(context.Background(), args, &stdout, &stderr, runner)
			if calls != 0 || code != 2 {
				t.Fatalf("calls=%d code=%d stdout=%s", calls, code, stdout.String())
			}
			// --json after the separator belongs to invalid remote input, never format selection.
			if args[len(args)-2] != "--" {
				r := cleanupDecode(t, stdout.Bytes())
				if r.Code != "cleanup_invalid" {
					t.Fatal(r)
				}
			}
		})
	}
	for _, args := range [][]string{{"--config", "cleanup", "doctor"}, {"exec", "i-12345678", "--", "cleanup"}, {"up", "agent", "--name", "cleanup"}} {
		if isCleanupCommand(args) {
			t.Fatal("misidentified command", args)
		}
	}
}
func TestCleanupDispatchGlobalsDeadlinesAndHelp(t *testing.T) {
	for _, duration := range []string{"", "50ms", "5m"} {
		args := []string{"--config", "selected.toml", "--aws-profile", "chosen", "cleanup", "--region=us-west-2", "--dry-run", "--json"}
		if duration != "" {
			args = append(args, "--timeout", duration)
		}
		calls := 0
		var stdout, stderr bytes.Buffer
		runner := func(ctx context.Context, path string, o config.Overrides, dry bool, w io.Writer) expiry.Result {
			calls++
			if path != "selected.toml" || o.AWSProfile != "chosen" || o.Region != "us-west-2" || !dry {
				t.Fatalf("%s %+v %t", path, o, dry)
			}
			deadline, ok := ctx.Deadline()
			want := cleanupTimeout
			if duration != "" {
				want, _ = time.ParseDuration(duration)
			}
			if !ok || time.Until(deadline) > want || time.Until(deadline) < want-time.Second {
				t.Fatal("bad deadline", deadline)
			}
			r := cleanupFailure(cleanupScope, true, "cleanup_no_candidates", "No expired candidates.", 0)
			r.OK = true
			r.Complete = true
			r.ScanComplete = true
			return r
		}
		if code := runCleanupCommand(context.Background(), args, &stdout, &stderr, runner); code != 0 || calls != 1 {
			t.Fatal(code, calls)
		}
		cleanupDecode(t, stdout.Bytes())
	}
	var stdout, stderr bytes.Buffer
	if code := runCleanupCommand(context.Background(), []string{"cleanup", "--help"}, &stdout, &stderr, func(context.Context, string, config.Overrides, bool, io.Writer) expiry.Result {
		t.Fatal("help invoked runner")
		return expiry.Result{}
	}); code != 0 || !strings.Contains(stdout.String(), "No launch manifest") {
		t.Fatal(code, stdout.String())
	}
}
