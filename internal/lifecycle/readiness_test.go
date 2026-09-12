package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	st "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

const probeID = "01234567-89ab-cdef-0123-456789abcdef"
const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type fakeSSM struct {
	*ssm.Client
	ping         st.PingStatus
	output       string
	denied       bool
	inFlight     bool
	invisible    int
	sent, polled int
	document     string
	badIdentity  bool
}

func (f *fakeSSM) GetDocument(_ context.Context, in *ssm.GetDocumentInput, _ ...func(*ssm.Options)) (*ssm.GetDocumentOutput, error) {
	return &ssm.GetDocumentOutput{Name: in.Name, DocumentVersion: in.DocumentVersion, DocumentType: st.DocumentTypeCommand, Status: st.DocumentStatusActive, Content: aws.String(f.document)}, nil
}
func (f *fakeSSM) DescribeInstanceInformation(_ context.Context, in *ssm.DescribeInstanceInformationInput, _ ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	out := &ssm.DescribeInstanceInformationOutput{}
	if f.ping != "" {
		out.InstanceInformationList = []st.InstanceInformation{{InstanceId: aws.String(in.Filters[0].Values[0]), PingStatus: f.ping}}
	}
	return out, nil
}
func (f *fakeSSM) SendCommand(_ context.Context, in *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	f.sent++
	if len(in.InstanceIds) != 1 || in.DocumentName == nil || aws.ToString(in.DocumentVersion) != "1" || len(in.Parameters) != 0 || in.OutputS3BucketName != nil || in.CloudWatchOutputConfig != nil {
		panic("unsafe probe contract")
	}
	if f.denied {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "SECRET"}
	}
	return &ssm.SendCommandOutput{Command: &st.Command{CommandId: aws.String(probeID)}}, nil
}
func (f *fakeSSM) GetCommandInvocation(_ context.Context, in *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	f.polled++
	if f.polled <= f.invisible {
		return nil, &smithy.GenericAPIError{Code: "InvocationDoesNotExist", Message: "SECRET"}
	}
	out := &ssm.GetCommandInvocationOutput{CommandId: in.CommandId, InstanceId: in.InstanceId, DocumentName: aws.String("probe"), DocumentVersion: aws.String("1"), PluginName: in.PluginName, Status: st.CommandInvocationStatusSuccess, ResponseCode: 0, StandardOutputContent: aws.String(f.output)}
	if f.inFlight {
		out.Status = st.CommandInvocationStatusInProgress
	}
	if f.badIdentity {
		out.InstanceId = aws.String("i-87654321")
	}
	return out, nil
}
func readinessSetup(t *testing.T) (*Service, config.Manifest, *fakeSSM) {
	t.Helper()
	api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return inventory(worker("i-12345678")), nil
	}}
	s := testService(api)
	s.PollInterval = time.Millisecond
	f := &fakeSSM{ping: st.PingStatusOnline, document: `{"schemaVersion":"2.2"}`, output: `{"schema_version":1,"bootstrap":"complete","host_key":"` + hostKey + `"}`}
	s.SSM = f
	sum := sha256.Sum256([]byte(f.document))
	m := config.Manifest{Readiness: config.Document{Name: "probe", Version: "1", ContentSHA256: hex.EncodeToString(sum[:])}}
	return s, m, f
}
func TestReadinessRequiresEverySignal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ping    st.PingStatus
		output  string
		ready   string
		wantErr bool
	}{
		{"online complete", st.PingStatusOnline, `{"schema_version":1,"bootstrap":"complete","host_key":"` + hostKey + `"}`, "ready", false},
		{"running without SSM", "", "", "pending", false},
		{"SSM lost", st.PingStatusConnectionLost, "", "pending", false},
		{"bootstrap failed", st.PingStatusOnline, `{"schema_version":1,"bootstrap":"failed","host_key":""}`, "failed", false},
		{"bootstrap pending", st.PingStatusOnline, `{"schema_version":1,"bootstrap":"pending","host_key":""}`, "pending", false},
		{"unknown output", st.PingStatusOnline, "SECRET", "unknown", true},
		{"invalid host", st.PingStatusOnline, `{"schema_version":1,"bootstrap":"complete","host_key":"SECRET"}`, "unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m, f := readinessSetup(t)
			f.ping, f.output = tc.ping, tc.output
			i := record(worker("i-12345678"))
			err := s.observe(context.Background(), m, &i)
			if (err != nil) != tc.wantErr || i.Readiness != tc.ready {
				t.Fatalf("%+v %v", i, err)
			}
			if tc.ping != st.PingStatusOnline && f.sent != 0 {
				t.Fatal("sent without online SSM")
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("leaked")
			}
		})
	}
}
func TestBoundedReadinessAndCommandIdentity(t *testing.T) {
	for _, tc := range []string{"SSM unavailable", "invocation in flight", "bootstrap failure", "denied", "wrong invocation"} {
		t.Run(tc, func(t *testing.T) {
			s, m, f := readinessSetup(t)
			switch tc {
			case "SSM unavailable":
				f.ping = ""
			case "invocation in flight":
				f.inFlight = true
			case "bootstrap failure":
				f.output = `{"schema_version":1,"bootstrap":"failed","host_key":""}`
			case "denied":
				f.denied = true
			case "wrong invocation":
				f.badIdentity = true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
			defer cancel()
			i := record(worker("i-12345678"))
			err := s.WaitReady(ctx, m, &i, io.Discard)
			if err == nil || i.ID != "i-12345678" || i.Readiness == "ready" {
				t.Fatalf("%+v %v", i, err)
			}
			if tc == "invocation in flight" && (f.sent != 1 || i.ProbeCommandID != probeID || !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("overlapping or lost command: %+v sent=%d %v", i, f.sent, err)
			}
			if tc == "denied" && (i.Bootstrap != "unknown" || i.ObservationCode != "probe_denied") {
				t.Fatalf("%+v", i)
			}
		})
	}
}
func TestProbeEventualConsistencyAndScopeRevalidation(t *testing.T) {
	s, m, f := readinessSetup(t)
	f.invisible = 2
	i := record(worker("i-12345678"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.WaitReady(ctx, m, &i, io.Discard); err != nil || f.sent != 1 || f.polled != 3 {
		t.Fatalf("%v %+v", err, f)
	}
	s, m, f = readinessSetup(t)
	s.Scope.Owner = "different-owner"
	i = record(worker("i-12345678"))
	if err := s.observe(ctx, m, &i); err == nil || f.sent != 0 {
		t.Fatal("scope mismatch dispatched a probe")
	}
}
func TestInventorySurvivesMissingManifestAndInvalidProfile(t *testing.T) {
	path := testutil.Setup(t)
	c, _ := config.Load(path, config.Overrides{})
	os.Remove(c.Manifest)
	testutil.Write(t, path, testutil.Config+"profile_file='missing.toml'\n")
	s, _, _ := readinessSetup(t)
	result := Run(context.Background(), path, config.Overrides{}, Options{Command: "ls"}, Dependencies{New: func(_ context.Context, c config.Config) (*Service, error) { s.Scope = c; return s, nil }}, io.Discard)
	if len(result.Instances) != 1 || result.Instances[0].ID != "i-12345678" || result.OK || result.Instances[0].Readiness != "unknown" {
		t.Fatalf("%+v", result)
	}
	b, _ := json.Marshal(result)
	if !json.Valid(b) {
		t.Fatal("invalid JSON")
	}
}

func TestDispatchedResumeRetainsAllocationWhenReadinessMetadataIsLost(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	r, err := newReceipt(parameters(s.Scope, m, p, "smoke"))
	if err != nil {
		t.Fatal(err)
	}
	r.State = "dispatched"
	release, err := store.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(r); err != nil {
		t.Fatal(err)
	}
	release()
	instance := launched(launchInput(r))
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return inventory(instance), nil
	}
	path := testutil.Setup(t)
	c, _ := config.Load(path, config.Overrides{})
	os.Remove(c.Manifest)
	result := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", UpOptions: UpOptions{Resume: r.RequestID}}, Dependencies{Store: &store, New: func(context.Context, config.Config) (*Service, error) { return s, nil }}, io.Discard)
	if result.OK || result.RequestID != r.RequestID || len(result.Instances) != 1 || result.Instances[0].ID == "" || api.launches != 0 {
		t.Fatalf("lost recovery: %+v launches=%d", result, api.launches)
	}
	saved, err := store.Load(r.RequestID)
	if err != nil || saved.State != "observed" || len(saved.InstanceIDs) != 1 {
		t.Fatalf("allocation not persisted: %+v %v", saved, err)
	}
}
