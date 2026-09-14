package lifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

type batchProbeBehavior struct {
	pending              int
	failed, denied, slow bool
}

type batchReadinessSSM struct {
	*ssm.Client
	mu                         sync.Mutex
	document                   string
	behaviors                  map[string]batchProbeBehavior
	sends, polls               map[string]int
	documents, active, maximum int
	deadlines                  []time.Time
	started                    chan string
	documentErr                error
}

func (s *batchReadinessSSM) deadline(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		panic("readiness operation has no shared deadline")
	}
	s.deadlines = append(s.deadlines, deadline)
}

func (s *batchReadinessSSM) GetDocument(ctx context.Context, in *ssm.GetDocumentInput, _ ...func(*ssm.Options)) (*ssm.GetDocumentOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline(ctx)
	s.documents++
	if s.documentErr != nil {
		return nil, s.documentErr
	}
	return &ssm.GetDocumentOutput{Name: in.Name, DocumentVersion: in.DocumentVersion, DocumentType: ssmtypes.DocumentTypeCommand, Status: ssmtypes.DocumentStatusActive, Content: aws.String(s.document)}, nil
}

func (s *batchReadinessSSM) DescribeInstanceInformation(ctx context.Context, in *ssm.DescribeInstanceInformationInput, _ ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	s.mu.Lock()
	s.deadline(ctx)
	s.mu.Unlock()
	id := in.Filters[0].Values[0]
	return &ssm.DescribeInstanceInformationOutput{InstanceInformationList: []ssmtypes.InstanceInformation{{InstanceId: aws.String(id), PingStatus: ssmtypes.PingStatusOnline}}}, nil
}

func (s *batchReadinessSSM) SendCommand(ctx context.Context, in *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline(ctx)
	if len(in.InstanceIds) != 1 || aws.ToString(in.DocumentName) != "probe" || aws.ToString(in.DocumentVersion) != "1" || len(in.Parameters) != 0 || in.OutputS3BucketName != nil || in.CloudWatchOutputConfig != nil {
		panic("unbounded probe dispatch")
	}
	id := in.InstanceIds[0]
	s.sends[id]++
	if s.behaviors[id].denied {
		return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "SECRET probe details"}
	}
	return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(probeID)}}, nil
}

func (s *batchReadinessSSM) GetCommandInvocation(ctx context.Context, in *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	id := aws.ToString(in.InstanceId)
	s.mu.Lock()
	s.deadline(ctx)
	s.polls[id]++
	poll := s.polls[id]
	behavior := s.behaviors[id]
	if behavior.slow {
		s.active++
		if s.active > s.maximum {
			s.maximum = s.active
		}
	}
	s.mu.Unlock()
	if behavior.slow {
		if s.started != nil {
			s.started <- id
		}
		<-ctx.Done()
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		return nil, ctx.Err()
	}
	bootstrap, key := "complete", hostKey
	if behavior.failed {
		bootstrap, key = "failed", ""
	} else if poll <= behavior.pending {
		bootstrap, key = "pending", ""
	}
	output := fmt.Sprintf(`{"schema_version":1,"bootstrap":%q,"host_key":%q}`, bootstrap, key)
	return &ssm.GetCommandInvocationOutput{CommandId: in.CommandId, InstanceId: in.InstanceId, DocumentName: aws.String("probe"), DocumentVersion: aws.String("1"), PluginName: in.PluginName, Status: ssmtypes.CommandInvocationStatusSuccess, ResponseCode: 0, StandardOutputContent: aws.String(output)}, nil
}

func batchReadinessFixture(t *testing.T, count int) (*Service, config.Manifest, []WorkerOutcome, *batchReadinessSSM, *fakeEC2, map[string]ec2types.Instance) {
	t.Helper()
	instances := map[string]ec2types.Instance{}
	workers := make([]WorkerOutcome, 0, count)
	for n := 0; n < count; n++ {
		id := fmt.Sprintf("i-%017x", n+1)
		instance := worker(id)
		instance.Tags = append(instance.Tags, ec2types.Tag{Key: aws.String("RequestId"), Value: aws.String(strings.Repeat("a", 32))}, ec2types.Tag{Key: aws.String("Profile"), Value: aws.String("agent")})
		instances[id] = instance
		workers = append(workers, WorkerOutcome{Instance: record(instance), Status: "allocated"})
	}
	api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		if len(in.InstanceIds) != 1 || len(in.Filters) != 0 {
			return nil, errors.New("expected exact ID scope check")
		}
		instance, ok := instances[in.InstanceIds[0]]
		if !ok {
			return inventory(), nil
		}
		return inventory(instance), nil
	}}
	s := testService(api)
	s.PollInterval = time.Millisecond
	ssm := &batchReadinessSSM{document: `{"schemaVersion":"2.2"}`, behaviors: map[string]batchProbeBehavior{}, sends: map[string]int{}, polls: map[string]int{}}
	s.SSM = ssm
	hash := sha256.Sum256([]byte(ssm.document))
	manifest := config.Manifest{Readiness: config.Document{Name: "probe", Version: "1", ContentSHA256: hex.EncodeToString(hash[:])}}
	return s, manifest, workers, ssm, api, instances
}

func TestWaitBatchReadyFailureDoesNotCancelPeersOrLosePins(t *testing.T) {
	s, m, workers, ssm, api, _ := batchReadinessFixture(t, 3)
	before := cloneWorkers(workers)
	ssm.behaviors[workers[0].ID] = batchProbeBehavior{failed: true}
	ssm.behaviors[workers[1].ID] = batchProbeBehavior{pending: 3}
	var progress bytes.Buffer
	err := s.WaitBatchReady(context.Background(), m, workers, &progress)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "bootstrap_failed" {
		t.Fatalf("failure not retained: %v", err)
	}
	if workers[0].Readiness != "failed" || workers[0].ObservationCode != "bootstrap_failed" || workers[1].Readiness != "ready" || workers[2].Readiness != "ready" {
		t.Fatalf("peer outcomes: %+v", workers)
	}
	if ssm.documents != 1 || ssm.polls[workers[1].ID] != 4 || api.launches != 0 || api.terminations != 0 {
		t.Fatalf("unexpected operations: docs=%d polls=%v launches=%d", ssm.documents, ssm.polls, api.launches)
	}
	for n, w := range workers {
		if w.ID != before[n].ID || w.RequestID != before[n].RequestID || w.Image != before[n].Image || w.Type != before[n].Type || w.Status != before[n].Status || !reflect.DeepEqual(w.Volumes, before[n].Volumes) {
			t.Fatal("readiness replaced known allocation pins")
		}
		if !strings.Contains(progress.String(), w.ID) || w.ProbeCommandID != probeID {
			t.Fatal("progress or probe recovery identity missing")
		}
	}
	for _, deadline := range ssm.deadlines {
		if !deadline.Equal(ssm.deadlines[0]) {
			t.Fatal("a poll started its own deadline")
		}
	}
	remaining := time.Until(ssm.deadlines[0])
	if remaining < 4*time.Minute+50*time.Second || remaining > 5*time.Minute {
		t.Fatalf("default batch deadline: %v", remaining)
	}
}

func TestWaitBatchReadySharedDeadlineBoundsFourAndRetainsQueuedWorkers(t *testing.T) {
	s, m, workers, ssm, api, _ := batchReadinessFixture(t, 9)
	for _, w := range workers {
		ssm.behaviors[w.ID] = batchProbeBehavior{slow: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	start := time.Now()
	err := s.WaitBatchReady(ctx, m, workers, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("batch multiplied deadline: %v", err)
	}
	if ssm.maximum != 4 || len(ssm.sends) != 4 {
		t.Fatalf("concurrency or queued dispatch: max=%d sends=%d", ssm.maximum, len(ssm.sends))
	}
	for _, w := range workers {
		if w.ID == "" || len(w.Volumes) != 1 || w.Readiness != "unknown" || w.ObservationCode != "observation_timeout" {
			t.Fatalf("queued identity lost: %+v", w)
		}
	}
	for _, observed := range ssm.deadlines {
		if !observed.Equal(deadline) {
			t.Fatal("worker deadline differed from outer deadline")
		}
	}
	if api.launches != 0 || api.terminations != 0 {
		t.Fatal("readiness mutated capacity")
	}
}

func TestWaitBatchReadySlowWorkerDoesNotCancelReadyPeer(t *testing.T) {
	s, m, workers, ssm, _, _ := batchReadinessFixture(t, 2)
	ssm.behaviors[workers[0].ID] = batchProbeBehavior{slow: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.WaitBatchReady(ctx, m, workers, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if workers[0].Readiness != "unknown" || workers[1].Readiness != "ready" || workers[1].HostKey != hostKey {
		t.Fatalf("ready peer canceled: %+v", workers)
	}
}

func TestWaitBatchReadyCancellationPreservesEveryWorker(t *testing.T) {
	s, m, workers, ssm, _, _ := batchReadinessFixture(t, 6)
	ssm.started = make(chan string, 6)
	for _, w := range workers {
		ssm.behaviors[w.ID] = batchProbeBehavior{slow: true}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.WaitBatchReady(ctx, m, workers, io.Discard) }()
	for n := 0; n < 4; n++ {
		select {
		case <-ssm.started:
		case <-time.After(time.Second):
			t.Fatal("four peers did not start")
		}
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, w := range workers {
		if w.ID == "" || w.ObservationCode != "observation_timeout" {
			t.Fatal("cancellation lost queued worker")
		}
	}
}

func TestWaitBatchReadySkipsHistoricalAndUnverifiedWorkers(t *testing.T) {
	s, m, workers, ssm, _, _ := batchReadinessFixture(t, 5)
	workers[0].Status = "historical"
	workers[0].State = "unknown"
	workers[1].State = "terminated"
	workers[2].Status = "identity_mismatch"
	workers[2].ObservationCode = "worker_scope_changed"
	workers[3].Status = "not_observed"
	if err := s.WaitBatchReady(context.Background(), m, workers, io.Discard); err == nil {
		t.Fatal("non-ready workers reported success")
	}
	if workers[0].Readiness != "not_ready" || workers[1].Readiness != "not_ready" || workers[2].Readiness != "unknown" || workers[2].ObservationCode != "worker_scope_changed" || workers[3].Readiness != "unknown" || workers[4].Readiness != "ready" {
		t.Fatalf("invalid mixed outcomes: %+v", workers)
	}
	if len(ssm.sends) != 1 || ssm.sends[workers[4].ID] != 1 {
		t.Fatal("unverified or historical worker was probed")
	}
}

func TestWaitBatchReadyScopeAndPinsCheckedBeforeProbe(t *testing.T) {
	for _, kind := range []string{"owner", "image", "request", "second-scope-check", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			s, m, workers, ssm, api, instances := batchReadinessFixture(t, 1)
			id := workers[0].ID
			i := instances[id]
			switch kind {
			case "owner":
				i.Tags[2].Value = aws.String("other")
			case "image":
				i.ImageId = aws.String("ami-87654321")
			case "request":
				i.Tags[len(i.Tags)-2].Value = aws.String(strings.Repeat("b", 32))
			case "duplicate":
				workers = append(workers, workers[0])
			case "second-scope-check":
				calls := 0
				api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					calls++
					if calls > 1 {
						i.Tags[2].Value = aws.String("other")
					}
					return inventory(i), nil
				}
			}
			instances[id] = i
			if err := s.WaitBatchReady(context.Background(), m, workers, io.Discard); err == nil || len(ssm.sends) != 0 {
				t.Fatalf("unsafe probe: %v sends=%v", err, ssm.sends)
			}
			for _, w := range workers {
				if w.ID != id || w.Image != "ami-12345678" || w.ObservationCode == "" {
					t.Fatal("failed validation lost original pins")
				}
			}
		})
	}
}

type batchProgressFailure struct {
	calls int
	short bool
}

func (w *batchProgressFailure) Write(b []byte) (int, error) {
	w.calls++
	if w.short {
		return len(b) - 1, nil
	}
	return 0, errors.New("SECRET writer detail")
}

func TestWaitBatchReadyOutputFailurePreservesReadyPeers(t *testing.T) {
	for _, short := range []bool{false, true} {
		s, m, workers, ssm, _, _ := batchReadinessFixture(t, 8)
		writer := &batchProgressFailure{short: short}
		err := s.WaitBatchReady(context.Background(), m, workers, writer)
		var f *Failure
		if !errors.As(err, &f) || f.Code != "output_unavailable" || strings.Contains(err.Error(), "SECRET") || writer.calls != 1 {
			t.Fatalf("output failure: %v writes=%d", err, writer.calls)
		}
		for _, w := range workers {
			if w.Readiness != "ready" || w.ID == "" || ssm.sends[w.ID] != 1 {
				t.Fatal("output failure canceled peers")
			}
		}
	}
}

func TestWaitBatchReadyProbeValidationFailureRetainsIDs(t *testing.T) {
	s, m, workers, ssm, _, _ := batchReadinessFixture(t, 3)
	ssm.documentErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "SECRET"}
	if err := s.WaitBatchReady(context.Background(), m, workers, io.Discard); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("document failure not sanitized")
	}
	if ssm.documents != 1 || len(ssm.sends) != 0 {
		t.Fatal("repeated verification or probe after rejection")
	}
	for _, w := range workers {
		if w.ID == "" || w.ObservationCode != "probe_denied" || w.Readiness != "unknown" {
			t.Fatal("probe rejection erased worker")
		}
	}
}

type batchBlockedProgress struct{ entered, release, finished chan struct{} }

func (w *batchBlockedProgress) Write(b []byte) (int, error) {
	close(w.entered)
	<-w.release
	close(w.finished)
	return len(b), nil
}

func TestWaitBatchReadyBlockedProgressCannotExtendDeadline(t *testing.T) {
	s, m, workers, ssm, _, _ := batchReadinessFixture(t, 12)
	writer := &batchBlockedProgress{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.WaitBatchReady(ctx, m, workers, writer) }()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("progress never started")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked writer extended batch deadline")
	}
	close(writer.release)
	<-writer.finished
	if len(ssm.sends) != len(workers) {
		t.Fatalf("blocked output prevented queued probes: observed %d of %d workers", len(ssm.sends), len(workers))
	}
	for _, w := range workers {
		if w.Readiness != "ready" || w.ID == "" {
			t.Fatal("blocked output erased successful readiness")
		}
	}
}

func TestWaitBatchReadyFinalLookupRevalidatesBatchPins(t *testing.T) {
	for _, changed := range []string{"request", "attempt", "added-group", "changed-group", "base-name", "legacy-name", "image", "type", "market", "subnet", "zone", "template", "template-version", "created-at"} {
		t.Run(changed, func(t *testing.T) {
			s, m, workers, ssm, api, _ := batchReadinessFixture(t, 1)
			id := workers[0].ID
			original := inventoryBatchWorker(id, "smoke", "", strings.Repeat("a", 32), "")
			inventoryDeleteTag(&original, "Group")
			if changed == "changed-group" {
				inventorySetTag(&original, "Group", "original")
			}
			if changed == "legacy-name" {
				original = worker(id)
			}
			workers[0] = WorkerOutcome{Instance: record(original), Status: "allocated"}
			expected := workers[0]
			calls := 0
			api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				calls++
				observed := original
				observed.Tags = append([]ec2types.Tag(nil), original.Tags...)
				if calls > 1 {
					switch changed {
					case "request":
						inventorySetTag(&observed, "RequestId", strings.Repeat("b", 32))
						inventorySetTag(&observed, "BatchId", strings.Repeat("b", 32))
					case "attempt":
						inventorySetTag(&observed, "AttemptId", strings.Repeat("b", 32))
					case "added-group", "changed-group":
						inventorySetTag(&observed, "Group", "different")
					case "base-name":
						inventorySetTag(&observed, "Name", "different")
						inventorySetTag(&observed, "BaseName", "different")
					case "legacy-name":
						inventorySetTag(&observed, "Name", "different")
					case "image":
						observed.ImageId = aws.String("ami-87654321")
					case "type":
						observed.InstanceType = ec2types.InstanceTypeC6i2xlarge
					case "market":
						observed.InstanceLifecycle = ""
					case "subnet":
						observed.SubnetId = aws.String("subnet-87654321")
					case "zone":
						observed.Placement = &ec2types.Placement{AvailabilityZone: aws.String("us-east-2b")}
					case "template":
						inventorySetTag(&observed, "aws:ec2launchtemplate:id", "lt-87654321")
					case "template-version":
						inventorySetTag(&observed, "aws:ec2launchtemplate:version", "5")
					case "created-at":
						inventorySetTag(&observed, "CreatedAt", "2026-09-15T12:00:00Z")
					}
				}
				return inventory(observed), nil
			}
			err := s.WaitBatchReady(context.Background(), m, workers, io.Discard)
			var f *Failure
			if !errors.As(err, &f) || f.Code != "worker_identity_changed" || calls != 2 || len(ssm.sends) != 0 {
				t.Fatalf("final lookup did not stop probe: err=%v lookups=%d sends=%v", err, calls, ssm.sends)
			}
			if workers[0].Readiness != "unknown" || workers[0].ObservationCode != "worker_identity_changed" || !batchReadyIdentity(expected.Instance, workers[0].Instance) || !reflect.DeepEqual(expected.Volumes, workers[0].Volumes) {
				t.Fatal("identity drift overwrote original worker evidence")
			}
		})
	}
}

func TestWaitBatchReadyObservesPendingAndNewTermination(t *testing.T) {
	for _, terminated := range []bool{false, true} {
		s, m, workers, ssm, api, instances := batchReadinessFixture(t, 1)
		id := workers[0].ID
		calls := 0
		api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			calls++
			i := instances[id]
			state := ec2types.InstanceStateNameRunning
			if terminated {
				state = ec2types.InstanceStateNameTerminated
			} else if calls == 1 {
				state = ec2types.InstanceStateNamePending
			}
			i.State = &ec2types.InstanceState{Name: state}
			return inventory(i), nil
		}
		err := s.WaitBatchReady(context.Background(), m, workers, io.Discard)
		if terminated {
			if err == nil || workers[0].State != "terminated" || workers[0].Readiness != "not_ready" || len(ssm.sends) != 0 {
				t.Fatalf("new termination lost: %+v %v", workers[0], err)
			}
		} else if err != nil || workers[0].Readiness != "ready" || ssm.sends[id] != 1 {
			t.Fatalf("pending worker did not become ready: %+v %v", workers[0], err)
		}
	}
}
