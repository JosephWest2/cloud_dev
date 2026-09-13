package execution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

const acknowledgedID = "01234567-89ab-cdef-0123-456789abcdef"

type dispatchTargets struct {
	resolve func(context.Context, string) ([]lifecycle.Instance, error)
	ready   func(*lifecycle.Instance) error
}

func (f dispatchTargets) Resolve(ctx context.Context, id string) ([]lifecycle.Instance, error) {
	return f.resolve(ctx, id)
}
func (f dispatchTargets) WaitReady(_ context.Context, _ config.Manifest, i *lifecycle.Instance, _ io.Writer) error {
	if f.ready != nil {
		return f.ready(i)
	}
	i.Readiness = "ready"
	return nil
}

type dispatchSSM struct {
	*ssm.Client
	document      string
	version       string
	wrongDocument bool
	wrongAgent    bool
	agentErr      error
	sendErr       error
	sendOutput    *ssm.SendCommandOutput
	sendActual    func(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	sent          []*ssm.SendCommandInput
	events        *[]string
}

func (f *dispatchSSM) GetDocument(_ context.Context, in *ssm.GetDocumentInput, _ ...func(*ssm.Options)) (*ssm.GetDocumentOutput, error) {
	name := aws.ToString(in.Name)
	if f.wrongDocument {
		name = "other"
	}
	return &ssm.GetDocumentOutput{Name: aws.String(name), DocumentVersion: in.DocumentVersion, DocumentType: ssmtypes.DocumentTypeCommand, Status: ssmtypes.DocumentStatusActive, Content: aws.String(f.document)}, nil
}
func (f *dispatchSSM) DescribeInstanceInformation(_ context.Context, in *ssm.DescribeInstanceInformationInput, _ ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	id := in.Filters[0].Values[0]
	if f.wrongAgent {
		id = "i-87654321"
	}
	return &ssm.DescribeInstanceInformationOutput{InstanceInformationList: []ssmtypes.InstanceInformation{{InstanceId: aws.String(id), PingStatus: ssmtypes.PingStatusOnline, AgentVersion: aws.String(f.version), PlatformType: ssmtypes.PlatformTypeLinux, ResourceType: ssmtypes.ResourceTypeEc2Instance}}}, f.agentErr
}
func (f *dispatchSSM) SendCommand(ctx context.Context, in *ssm.SendCommandInput, options ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	*f.events = append(*f.events, "send")
	f.sent = append(f.sent, in)
	if f.sendActual != nil {
		return f.sendActual(ctx, in, options...)
	}
	o := ssm.Options{}
	for _, apply := range options {
		apply(&o)
	}
	if o.RetryMaxAttempts != 1 || o.Retryer == nil || o.Retryer.MaxAttempts() != 1 {
		panic("dispatch must disable retries on the actual SDK operation")
	}
	if f.sendOutput != nil || f.sendErr != nil {
		return f.sendOutput, f.sendErr
	}
	return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(acknowledgedID)}}, nil
}

type dispatchArtifact struct {
	*s3.Client
	hash    string
	invalid bool
}

func (f dispatchArtifact) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if aws.ToString(in.ExpectedBucketOwner) != "123456789012" || aws.ToString(in.Key) != "artifacts/runner/"+f.hash+"/linux-amd64" {
		panic("unscoped runner artifact access")
	}
	if f.invalid {
		return &s3.HeadObjectOutput{}, nil
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(10), Metadata: map[string]string{"sha256": f.hash}, ServerSideEncryption: s3types.ServerSideEncryptionAes256}, nil
}

type dispatchStore struct {
	values  map[string][]byte
	puts    int
	putErr  error
	ackErr  error
	persist bool
	corrupt bool
	events  *[]string
}

func (s *dispatchStore) Get(_ context.Context, key string) (execprotocol.Object, error) {
	b, ok := s.values[key]
	if !ok {
		return execprotocol.Object{}, execprotocol.ErrDenied
	}
	return execprotocol.Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b)), LastModified: time.Now().UTC()}, nil
}
func (s *dispatchStore) Put(_ context.Context, key string, data io.ReadSeeker, size int64, digest string) error {
	s.puts++
	kind := "request"
	putErr := s.putErr
	if strings.HasSuffix(key, "/acknowledgement.json") {
		kind, putErr = "acknowledgement", s.ackErr
	}
	*s.events = append(*s.events, kind)
	b, err := io.ReadAll(data)
	if err != nil || int64(len(b)) != size || execprotocol.Digest(b) != digest {
		panic("invalid immutable PUT bytes")
	}
	if putErr == nil || s.persist {
		s.values[key] = b
	}
	if s.corrupt {
		s.values[key] = []byte("contradiction")
	}
	return putErr
}

type eventWriter struct {
	bytes.Buffer
	events *[]string
	failOn string
}

func (w *eventWriter) Write(p []byte) (int, error) {
	state := "prepared-output"
	if bytes.Contains(p, []byte("submitted")) {
		state = "submitted-output"
	}
	*w.events = append(*w.events, state)
	if w.failOn == state {
		return 0, errors.New("SECRET output error")
	}
	return w.Buffer.Write(p)
}

func dispatchSetup(t *testing.T) (*Service, *dispatchSSM, *dispatchStore, *eventWriter, execprotocol.Payload) {
	t.Helper()
	var m config.Manifest
	if err := json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	m.Execution.ContentSHA256 = execprotocol.Digest([]byte(`{"schemaVersion":"2.2"}`))
	events := []string{}
	ssmAPI := &dispatchSSM{document: `{"schemaVersion":"2.2"}`, version: "3.3.2746.0", events: &events}
	store := &dispatchStore{values: map[string][]byte{}, events: &events}
	i := lifecycle.Instance{ID: "i-12345678", Image: m.Images["agent"].AMIID, TemplateID: m.Images["agent"].LaunchTemplateID, TemplateVersion: m.Images["agent"].LaunchTemplateVersion, State: "running"}
	targets := dispatchTargets{resolve: func(context.Context, string) ([]lifecycle.Instance, error) { return []lifecycle.Instance{i}, nil }}
	s := &Service{Scope: config.Config{ExpectedAccount: m.Account, Region: m.Region, Deployment: m.Deployment, Owner: m.Owner}, Manifest: m, Targets: targets, SSM: ssmAPI, Store: store, Storage: dispatchArtifact{hash: m.Execution.RunnerSHA256}, CheckStorage: func(context.Context) error { return nil }}
	p, err := Prepare([]string{"printf", "", "two words", "\"quoted\"", "line\nfeed", "☃", "$(touch SECRET)", "--json", "--help", "--timeout", "--"}, "project/../project", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s, ssmAPI, store, &eventWriter{events: &events}, p
}

func TestSubmitLiteralPayloadAndDurableAnnouncementOrder(t *testing.T) {
	s, api, store, progress, payload := dispatchSetup(t)
	var resolved []string
	initial := s.Targets.(dispatchTargets)
	s.Targets = dispatchTargets{resolve: func(ctx context.Context, target string) ([]lifecycle.Instance, error) {
		resolved = append(resolved, target)
		return initial.Resolve(ctx, target)
	}}
	sub, err := s.Submit(context.Background(), "smoke", payload, 300, progress)
	if err != nil || sub.State != "submitted" || sub.SSMCommandID != acknowledgedID || !sub.AcknowledgementStored {
		t.Fatalf("submission = %+v, %v", sub, err)
	}
	if !reflect.DeepEqual(resolved, []string{"smoke", "i-12345678"}) || len(api.sent) != 1 {
		t.Fatalf("resolve %v sends %d", resolved, len(api.sent))
	}
	in := api.sent[0]
	if !reflect.DeepEqual(in.InstanceIds, []string{"i-12345678"}) || len(in.Targets) != 0 || aws.ToString(in.DocumentName) != s.Manifest.Execution.Name || aws.ToString(in.DocumentVersion) != s.Manifest.Execution.Version || aws.ToString(in.DocumentHash) != s.Manifest.Execution.ContentSHA256 || in.DocumentHashType != ssmtypes.DocumentHashTypeSha256 || aws.ToInt32(in.TimeoutSeconds) != 300 || len(in.Parameters) != 3 || in.Parameters["requestId"][0] != sub.Binding.CommandID || in.Parameters["stepTimeoutSeconds"][0] != "3780" || in.OutputS3BucketName != nil || in.CloudWatchOutputConfig != nil || in.ServiceRoleArn != nil {
		t.Fatalf("unscoped/unpinned request: %+v", in)
	}
	decoded, err := execprotocol.DecodePayload(in.Parameters["payload"][0])
	if err != nil || !reflect.DeepEqual(decoded, payload) || decoded.Cwd != "/home/devbox/project" {
		t.Fatalf("payload changed: %+v %v", decoded, err)
	}
	data, _ := base64.StdEncoding.DecodeString(in.Parameters["payload"][0])
	if sub.Binding.PayloadSHA256 != execprotocol.Digest(data) || !execprotocol.ValidCommandID(sub.Binding.CommandID) {
		t.Fatal("invalid public identity/hash")
	}
	for _, kind := range []string{"request", "acknowledgement"} {
		r, _, err := execprotocol.ReadRecord(context.Background(), store, execprotocol.ObjectKey(sub.Binding.Scope, sub.Binding.CommandID, kind+".json"))
		if err != nil || r.Binding != sub.Binding || r.Kind != kind || strings.Contains(string(store.values[execprotocol.ObjectKey(sub.Binding.Scope, sub.Binding.CommandID, kind+".json")]), "SECRET") {
			t.Fatalf("record %s invalid: %+v %v", kind, r, err)
		}
	}
	want := []string{"request", "prepared-output", "send", "submitted-output", "acknowledgement"}
	if !reflect.DeepEqual(*progress.events, want) || strings.Contains(progress.String(), "SECRET") {
		t.Fatalf("announcement ordering %v, output %q", *progress.events, progress.String())
	}
}

func TestPrepareUsesRemotePOSIXPathsAndRejectsInvalidBytes(t *testing.T) {
	for cwd, want := range map[string]string{"": "/home/devbox", ".": "/home/devbox", "../other": "/home/other", "/tmp/../etc": "/etc", "~/project": "/home/devbox/~/project", "$HOME": "/home/devbox/$HOME"} {
		p, err := Prepare([]string{"true"}, cwd, time.Second)
		if err != nil || p.Cwd != want {
			t.Fatalf("cwd %q = %q %v", cwd, p.Cwd, err)
		}
	}
	for _, tc := range []struct {
		argv    []string
		cwd     string
		timeout time.Duration
	}{
		{nil, "", time.Second}, {[]string{""}, "", time.Second}, {[]string{"true", string([]byte{0xff})}, "", time.Second}, {[]string{"true", "\x00"}, "", time.Second}, {[]string{"true"}, "bad\x00/..", time.Second}, {[]string{"true"}, string([]byte{0xff}), time.Second}, {[]string{"true", strings.Repeat("x", 32768)}, "", time.Second}, {[]string{"true"}, "", 0}, {[]string{"true"}, "", time.Second + 1}, {[]string{"true"}, "", 24*time.Hour + time.Second},
	} {
		if _, err := Prepare(tc.argv, tc.cwd, tc.timeout); err == nil {
			t.Fatalf("accepted invalid input %+v", tc)
		}
	}
}

func TestSubmitPreflightFailureNeverDispatches(t *testing.T) {
	for _, scenario := range []string{"invalid payload", "invalid delivery", "invalid target", "wrong scope", "stale template", "changed target", "readiness failed", "storage denied", "document changed", "document identity", "artifact invalid", "old agent", "malformed agent", "different agent", "agent denied", "request denied", "request contradiction", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			s, api, store, progress, payload := dispatchSetup(t)
			ctx := context.Background()
			delivery, target := 300, "smoke"
			original := s.Targets.(dispatchTargets)
			switch scenario {
			case "invalid payload":
				payload.Argv = []string{""}
			case "invalid delivery":
				delivery = 29
			case "invalid target":
				target = "../SECRET"
			case "wrong scope":
				s.Manifest.Owner = "other"
			case "stale template":
				s.Targets = dispatchTargets{resolve: func(ctx context.Context, id string) ([]lifecycle.Instance, error) {
					instances, _ := original.Resolve(ctx, id)
					instances[0].TemplateVersion = "99"
					return instances, nil
				}}
			case "changed target":
				s.Targets = dispatchTargets{resolve: func(ctx context.Context, id string) ([]lifecycle.Instance, error) {
					instances, _ := original.Resolve(ctx, id)
					if id == "i-12345678" {
						instances[0].State = "stopped"
					}
					return instances, nil
				}}
			case "readiness failed":
				original.ready = func(*lifecycle.Instance) error {
					return &lifecycle.Failure{Code: "bootstrap_failed", Message: "bootstrap failed"}
				}
				s.Targets = original
			case "storage denied":
				s.CheckStorage = func(context.Context) error { return errors.New("SECRET") }
			case "document changed":
				api.document = `{}`
			case "document identity":
				api.wrongDocument = true
			case "artifact invalid":
				s.Storage = dispatchArtifact{hash: s.Manifest.Execution.RunnerSHA256, invalid: true}
			case "old agent":
				api.version = "3.3.2745.99"
			case "malformed agent":
				api.version = "3.3.2746.0junk"
			case "different agent":
				api.wrongAgent = true
			case "agent denied":
				api.agentErr = errors.New("SECRET")
			case "request denied":
				store.putErr = execprotocol.ErrDenied
			case "request contradiction":
				store.putErr, store.corrupt = execprotocol.ErrUnavailable, true
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := s.Submit(ctx, target, payload, delivery, progress)
			if err == nil || len(api.sent) != 0 || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error %v, sends %d", err, len(api.sent))
			}
			if scenario != "request denied" && scenario != "request contradiction" && scenario != "changed target" && store.puts != 0 {
				t.Fatalf("preflight wrote %d objects", store.puts)
			}
		})
	}
}

func TestSubmitAcknowledgementAndUncertainPublication(t *testing.T) {
	for _, scenario := range []string{"request response lost", "ack denied", "ack response lost", "send lost", "send denied", "malformed acknowledgement", "wrong acknowledgement target", "prepared output failed", "submitted output failed"} {
		t.Run(scenario, func(t *testing.T) {
			s, api, store, progress, payload := dispatchSetup(t)
			wantState, wantError, wantAck, wantSends := "submitted", false, true, 1
			switch scenario {
			case "request response lost":
				store.putErr, store.persist = execprotocol.ErrUnavailable, true
			case "ack denied":
				store.ackErr, wantAck = execprotocol.ErrDenied, false
			case "ack response lost":
				store.ackErr, store.persist = execprotocol.ErrUnavailable, true
			case "send lost":
				api.sendErr = errors.New("SECRET lost response")
				wantState, wantError, wantAck = "submission_unknown", true, false
			case "send denied":
				api.sendErr = &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "SECRET"}
				wantState, wantError, wantAck = "submission_rejected", true, false
			case "malformed acknowledgement":
				api.sendOutput = &ssm.SendCommandOutput{}
				wantState, wantError, wantAck = "submission_unknown", true, false
			case "wrong acknowledgement target":
				api.sendOutput = &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(acknowledgedID), InstanceIds: []string{"i-87654321"}}}
				wantState, wantError, wantAck = "submission_unknown", true, false
			case "prepared output failed":
				progress.failOn = "prepared-output"
				wantState, wantError, wantAck, wantSends = "prepared", true, false, 0
			case "submitted output failed":
				progress.failOn = "submitted-output"
				wantError, wantAck = true, false
			}
			sub, err := s.Submit(context.Background(), "smoke", payload, 300, progress)
			if (err != nil) != wantError || sub.State != wantState || sub.AcknowledgementStored != wantAck || len(api.sent) != wantSends || !execprotocol.ValidCommandID(sub.Binding.CommandID) || (err != nil && strings.Contains(err.Error(), "SECRET")) {
				t.Fatalf("sub=%+v err=%v sends=%d", sub, err, len(api.sent))
			}
			if sub.State == "submitted" && sub.SSMCommandID != acknowledgedID {
				t.Fatal("lost acknowledged ID")
			}
			if scenario == "ack denied" && sub.DiagnosticCode != "acknowledgement_publication_failed" {
				t.Fatal("missing mapping diagnostic")
			}
			if (wantState == "submission_unknown" || wantState == "submission_rejected") && store.puts != 1 {
				t.Fatal("wrote fabricated acknowledgement")
			}
		})
	}
}

type lostResponseTransport struct {
	calls int
	body  []byte
}

func (t *lostResponseTransport) Do(r *http.Request) (*http.Response, error) {
	t.calls++
	t.body, _ = io.ReadAll(r.Body)
	return nil, errors.New("connection reset after accepting non-idempotent request")
}

func TestActualSDKLostSendResponseMakesOneWireSubmission(t *testing.T) {
	s, api, store, progress, payload := dispatchSetup(t)
	transport := &lostResponseTransport{}
	realClient := ssm.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "TEST", SecretAccessKey: "TEST"}, nil
	}), HTTPClient: transport, RetryMaxAttempts: 5})
	api.sendActual = realClient.SendCommand
	sub, err := s.Submit(context.Background(), "smoke", payload, 300, progress)
	var f *Failure
	if !errors.As(err, &f) || f.Code != "submission_unknown" || sub.State != "submission_unknown" || transport.calls != 1 || store.puts != 1 || sub.SSMCommandID != "" {
		t.Fatalf("sub=%+v err=%v wire=%d puts=%d", sub, err, transport.calls, store.puts)
	}
	var wire struct {
		Parameters                    map[string][]string
		InstanceIds                   []string
		DocumentHash, DocumentVersion string
	}
	if json.Unmarshal(transport.body, &wire) != nil || len(wire.InstanceIds) != 1 || wire.InstanceIds[0] != "i-12345678" || wire.Parameters["requestId"][0] != sub.Binding.CommandID || wire.DocumentHash != s.Manifest.Execution.ContentSHA256 || wire.DocumentVersion != s.Manifest.Execution.Version {
		t.Fatal("wire request lost scope/pins/public recovery ID")
	}
}

type resolverEC2 struct {
	*ec2.Client
	response *ec2.DescribeInstancesOutput
}

func (f resolverEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f.response, nil
}

func TestDispatchUsesLifecycleResolverScopeAndAmbiguityGuards(t *testing.T) {
	for _, scenario := range []string{"wrong owner", "wrong account", "wrong explicit id", "ambiguous name", "unmanaged"} {
		t.Run(scenario, func(t *testing.T) {
			s, api, store, progress, payload := dispatchSetup(t)
			worker := ec2types.Instance{InstanceId: aws.String("i-12345678"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}, Tags: []ec2types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String(s.Scope.Deployment)}, {Key: aws.String("Owner"), Value: aws.String(s.Scope.Owner)}, {Key: aws.String("Name"), Value: aws.String("smoke")}}}
			out := &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{OwnerId: aws.String(s.Scope.ExpectedAccount), Instances: []ec2types.Instance{worker}}}}
			target := "smoke"
			switch scenario {
			case "wrong owner":
				out.Reservations[0].Instances[0].Tags[2].Value = aws.String("other")
			case "wrong account":
				out.Reservations[0].OwnerId = aws.String("999999999999")
			case "wrong explicit id":
				target = "i-87654321"
			case "ambiguous name":
				other := worker
				other.InstanceId = aws.String("i-87654321")
				out.Reservations[0].Instances = append(out.Reservations[0].Instances, other)
			case "unmanaged":
				out.Reservations[0].Instances[0].Tags[0].Value = aws.String("someone-else")
			}
			resolver := &lifecycle.Service{Scope: s.Scope, API: resolverEC2{response: out}}
			s.Targets = dispatchTargets{resolve: resolver.Resolve}
			_, err := s.Submit(context.Background(), target, payload, 300, progress)
			if err == nil || len(api.sent) != 0 || store.puts != 0 {
				t.Fatalf("err=%v sends=%d puts=%d", err, len(api.sent), store.puts)
			}
		})
	}
}

func TestAgentVersionComparison(t *testing.T) {
	for version, want := range map[string]bool{"3.3.2746.0": true, "3.3.2746.1": true, "3.4.0.0": true, "4.0.0.0": true, "3.3.2745.999": false, "3.3.2746": false, "03.3.2746.0": false, "3.3.2746.0x": false, "3.3.4294967296.0": false} {
		if agentSupported(version, "3.3.2746.0") != want {
			t.Fatalf("version %s", version)
		}
	}
}
