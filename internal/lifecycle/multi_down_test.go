package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type downTestCloud struct {
	mu             sync.Mutex
	workers        map[string]types.Instance
	terminated     []string
	keepRunning    map[string]bool
	disappear      map[string]bool
	terminationErr map[string]error
	describeHook   func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error, bool)
	volumeHook     func(context.Context, *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	terminateHook  func(context.Context, *ec2.TerminateInstancesInput) error
}

func newDownCloud(ids ...string) *downTestCloud {
	c := &downTestCloud{workers: map[string]types.Instance{}, keepRunning: map[string]bool{}, disappear: map[string]bool{}, terminationErr: map[string]error{}}
	for _, id := range ids {
		i := worker(id)
		i.BlockDeviceMappings[0].Ebs.VolumeId = aws.String("vol-" + strings.TrimPrefix(id, "i-"))
		c.workers[id] = i
	}
	return c
}

func downService(c *downTestCloud) *Service {
	return &Service{API: c, Scope: config.Config{ExpectedAccount: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "test-owner"}, PollInterval: time.Nanosecond}
}

func (c *downTestCloud) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if c.describeHook != nil {
		if out, err, handled := c.describeHook(ctx, in); handled {
			return out, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []types.Instance
	for id, i := range c.workers {
		if len(in.InstanceIds) != 0 && (len(in.InstanceIds) != 1 || in.InstanceIds[0] != id) {
			continue
		}
		tags := tagsOf(i)
		match := true
		for _, filter := range in.Filters {
			if strings.HasPrefix(aws.ToString(filter.Name), "tag:") && (len(filter.Values) != 1 || tags[strings.TrimPrefix(aws.ToString(filter.Name), "tag:")] != filter.Values[0]) {
				match = false
			}
		}
		if match {
			found = append(found, i)
		}
	}
	return inventory(found...), nil
}

func (c *downTestCloud) DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if c.volumeHook != nil {
		return c.volumeHook(ctx, in)
	}
	return nil, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound"}
}

func (c *downTestCloud) TerminateInstances(ctx context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	c.mu.Lock()
	c.terminated = append(c.terminated, in.InstanceIds...)
	c.mu.Unlock()
	if c.terminateHook != nil {
		if err := c.terminateHook(ctx, in); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range in.InstanceIds {
		if err := c.terminationErr[id]; err != nil {
			return nil, err
		}
		if c.disappear[id] {
			delete(c.workers, id)
		} else if i, ok := c.workers[id]; ok && !c.keepRunning[id] {
			i.State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
			i.BlockDeviceMappings = nil
			c.workers[id] = i
		}
	}
	return &ec2.TerminateInstancesOutput{}, nil
}

func (c *downTestCloud) RunInstances(context.Context, *ec2.RunInstancesInput, ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	panic("teardown attempted allocation")
}

func downResultWorker(t *testing.T, result TeardownOutcome, id string) TeardownWorker {
	t.Helper()
	for _, worker := range result.Workers {
		if worker.ID == id {
			return worker
		}
	}
	t.Fatalf("worker %s missing from %+v", id, result)
	return TeardownWorker{}
}

func TestMultiDownIndependentTargetsDeduplicationAndAmbiguity(t *testing.T) {
	c := newDownCloud("i-11111111", "i-22222222", "i-33333333", "i-44444444")
	for _, id := range []string{"i-22222222", "i-33333333"} {
		i := c.workers[id]
		inventorySetTag(&i, "Name", "ambiguous")
		c.workers[id] = i
	}
	i := c.workers["i-44444444"]
	inventorySetTag(&i, "Owner", "another-owner")
	c.workers["i-44444444"] = i
	s := downService(c)
	plan, err := s.SelectDown(context.Background(), DownSelection{Targets: []string{"smoke", "i-11111111", "smoke", "missing", "i-44444444", "ambiguous"}})
	if err != nil || len(plan.Candidates()) != 1 || len(c.terminated) != 0 || len(plan.Outcome().Errors) != 3 {
		t.Fatalf("selection %+v %v", plan.Outcome(), err)
	}
	result, err := s.ExecuteDown(context.Background(), plan)
	if err != nil || result.Status != "teardown_partial" || result.SelectedCount != 1 || result.CleanedCount != 1 || len(c.terminated) != 1 || c.terminated[0] != "i-11111111" {
		t.Fatalf("unsafe partial cleanup: %+v %v targets=%v", result, err, c.terminated)
	}
	for _, id := range []string{"i-22222222", "i-33333333", "i-44444444"} {
		if w := downResultWorker(t, result, id); len(w.Errors) == 0 {
			t.Fatalf("resolution evidence lost: %+v", w)
		}
	}
}

func TestMultiDownIncompleteScopeSelectionAuthorizesNothing(t *testing.T) {
	for _, mode := range []string{"failure", "cycle", "page_bound"} {
		t.Run(mode, func(t *testing.T) {
			c := newDownCloud("i-11111111")
			calls := 0
			c.describeHook = func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error, bool) {
				calls++
				if calls > 1 && mode == "failure" {
					return nil, errors.New("PRIVATE"), true
				}
				out := inventory(worker("i-11111111"))
				out.NextToken = aws.String("next")
				if mode == "page_bound" {
					out.NextToken = aws.String(fmt.Sprintf("page-%d", calls))
				}
				return out, nil, true
			}
			s := downService(c)
			plan, err := s.SelectDown(context.Background(), DownSelection{All: true, Yes: true})
			if err == nil || len(plan.Candidates()) != 0 || len(plan.Outcome().Workers) != 1 || calls > teardownInventoryPages {
				t.Fatalf("incomplete scan authorized workers: %+v %v calls=%d", plan.Outcome(), err, calls)
			}
			result, _ := s.ExecuteDown(context.Background(), plan)
			if len(c.terminated) != 0 || result.CleanedCount != 0 || strings.Contains(fmt.Sprint(result), "PRIVATE") {
				t.Fatalf("unsafe incomplete scan result: %+v", result)
			}
		})
	}
}

func TestMultiDownFinalExactReadRejectsDrift(t *testing.T) {
	for _, drift := range []string{"owner", "deployment", "managed", "account", "name", "group", "different_id", "region", "disappeared"} {
		t.Run(drift, func(t *testing.T) {
			c := newDownCloud("i-11111111")
			i := c.workers["i-11111111"]
			inventorySetTag(&i, "Group", "batch")
			c.workers["i-11111111"] = i
			selection := DownSelection{Targets: []string{"smoke"}}
			if drift == "group" {
				selection = DownSelection{Group: "batch"}
			}
			s := downService(c)
			plan, err := s.SelectDown(context.Background(), selection)
			if err != nil || len(plan.Candidates()) != 1 {
				t.Fatal(err, plan.Outcome())
			}
			calls := 0
			c.describeHook = func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error, bool) {
				calls++
				if len(in.InstanceIds) != 1 || in.InstanceIds[0] != "i-11111111" || len(in.Filters) != 0 {
					t.Errorf("post-selection scan or wrong ID: %+v", in)
				}
				i := worker("i-11111111")
				inventorySetTag(&i, "Group", "batch")
				out := inventory(i)
				switch drift {
				case "owner":
					inventorySetTag(&out.Reservations[0].Instances[0], "Owner", "other")
				case "deployment":
					inventorySetTag(&out.Reservations[0].Instances[0], "Deployment", "other")
				case "managed":
					inventorySetTag(&out.Reservations[0].Instances[0], "ManagedBy", "other")
				case "name":
					inventorySetTag(&out.Reservations[0].Instances[0], "Name", "other")
				case "group":
					inventorySetTag(&out.Reservations[0].Instances[0], "Group", "other")
				case "account":
					out.Reservations[0].OwnerId = aws.String("999999999999")
				case "different_id":
					out.Reservations[0].Instances[0].InstanceId = aws.String("i-22222222")
				case "disappeared":
					out = inventory()
				}
				return out, nil, true
			}
			if drift == "region" {
				s.Scope.Region = "us-west-2"
			}
			result, _ := s.ExecuteDown(context.Background(), plan)
			if len(c.terminated) != 0 || result.CleanedCount != 0 || len(result.Errors) == 0 || calls > 1 || len(result.Workers[0].Volumes) == 0 {
				t.Fatalf("drift lost safety/evidence: %+v calls=%d", result, calls)
			}
		})
	}
}

func TestMultiDownFrozenPlanCannotExpandOrBeChangedThroughPreview(t *testing.T) {
	c := newDownCloud("i-11111111")
	s := downService(c)
	plan, err := s.SelectDown(context.Background(), DownSelection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	preview := plan.Candidates()
	preview[0].ID = "i-22222222"
	preview[0].Volumes[0].ID = "vol-99999999"
	out := plan.Outcome()
	out.Workers[0].ID = "i-22222222"
	out.Workers[0].Volumes[0].ID = "vol-99999999"
	c.workers["i-22222222"] = worker("i-22222222")
	result, err := s.ExecuteDown(context.Background(), plan)
	if err != nil || result.Status != "teardown_complete" || len(c.terminated) != 1 || c.terminated[0] != "i-11111111" || result.Workers[0].Volumes[0].ID != "vol-11111111" {
		t.Fatalf("frozen plan changed: %+v %v targets=%v", result, err, c.terminated)
	}
}

func TestMultiDownMixedTerminationOutcomesPreservePeers(t *testing.T) {
	c := newDownCloud("i-11111111", "i-22222222", "i-33333333", "i-44444444", "i-55555555", "i-66666666", "i-77777777")
	c.terminationErr["i-22222222"] = &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "PRIVATE"}
	c.terminationErr["i-33333333"] = &smithy.GenericAPIError{Code: "OperationNotPermitted", Message: "PRIVATE"}
	c.terminationErr["i-44444444"] = errors.New("PRIVATE lost ACK")
	c.disappear["i-55555555"] = true
	c.keepRunning["i-77777777"] = true
	i := c.workers["i-66666666"]
	i.State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
	c.workers["i-66666666"] = i
	s := downService(c)
	plan, err := s.SelectDown(context.Background(), DownSelection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ExecuteDown(context.Background(), plan)
	if err != nil || result.Status != "teardown_partial" || result.CleanedCount != 2 || result.TerminatedCount != 2 || len(c.terminated) != 6 || strings.Contains(fmt.Sprint(result), "PRIVATE") {
		t.Fatalf("mixed results: %+v %v targets=%v", result, err, c.terminated)
	}
	for id, status := range map[string]string{"i-11111111": "termination_observed", "i-22222222": "termination_denied", "i-33333333": "termination_denied", "i-44444444": "termination_unknown", "i-55555555": "termination_requested", "i-66666666": "already_terminated", "i-77777777": "termination_requested"} {
		if got := downResultWorker(t, result, id); got.Status != status || len(got.Volumes) != 1 {
			t.Fatalf("worker evidence: %+v wanted %s", got, status)
		}
	}
}

func TestMultiDownRootDeletionRequiresExactPositiveEvidence(t *testing.T) {
	for _, mode := range []string{"not_found", "deleted", "empty", "wrong_id", "denied", "retained", "missing_mapping", "still_present", "conflicting_not_found", "pagination", "foreign_owner", "wrong_arn", "wrong_region", "duplicate", "missing_id", "deleted_attached"} {
		t.Run(mode, func(t *testing.T) {
			c := newDownCloud("i-11111111")
			i := c.workers["i-11111111"]
			if mode == "missing_mapping" {
				i.BlockDeviceMappings = nil
			}
			if mode == "retained" {
				i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
			}
			c.workers["i-11111111"] = i
			calls := 0
			c.volumeHook = func(_ context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
				calls++
				if len(in.VolumeIds) != 1 || in.VolumeIds[0] != "vol-11111111" {
					t.Errorf("unexpected volume observation: %+v", in)
				}
				out := &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-11111111"), State: types.VolumeStateAvailable}}}
				switch mode {
				case "not_found":
					return nil, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound"}
				case "deleted":
					out.Volumes[0].State = types.VolumeStateDeleted
				case "empty":
					out.Volumes = nil
				case "wrong_id":
					out.Volumes[0].VolumeId = aws.String("vol-22222222")
				case "denied":
					return nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "PRIVATE"}
				case "conflicting_not_found":
					return out, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound"}
				case "pagination":
					out.NextToken = aws.String("unread-page")
					out.Volumes[0].State = types.VolumeStateDeleted
				case "foreign_owner":
					out.Volumes[0].OwnerId = aws.String("999999999999")
					out.Volumes[0].State = types.VolumeStateDeleted
				case "wrong_arn":
					out.Volumes[0].VolumeArn = aws.String("arn:aws:ec2:us-east-2:999999999999:volume/vol-11111111")
					out.Volumes[0].State = types.VolumeStateDeleted
				case "wrong_region":
					out.Volumes[0].AvailabilityZone = aws.String("us-west-2a")
					out.Volumes[0].State = types.VolumeStateDeleted
				case "duplicate":
					out.Volumes[0].State = types.VolumeStateDeleted
					out.Volumes = append(out.Volumes, out.Volumes[0])
				case "missing_id":
					out.Volumes[0].VolumeId = nil
					out.Volumes[0].State = types.VolumeStateDeleted
				case "deleted_attached":
					out.Volumes[0].State = types.VolumeStateDeleted
					out.Volumes[0].Attachments = []types.VolumeAttachment{{State: types.VolumeAttachmentStateAttached, InstanceId: aws.String("i-22222222")}}
				}
				return out, nil
			}
			s := downService(c)
			plan, err := s.SelectDown(context.Background(), DownSelection{Targets: []string{"i-11111111"}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.ExecuteDown(context.Background(), plan)
			cleaned := mode == "not_found" || mode == "deleted"
			if err != nil || result.TerminatedCount != 1 || (result.CleanedCount == 1) != cleaned || (len(result.Errors) == 0) != cleaned || strings.Contains(fmt.Sprint(result), "PRIVATE") {
				t.Fatalf("false cleanup: %+v %v", result, err)
			}
			if (mode == "missing_mapping" || mode == "retained") && calls != 0 {
				t.Fatal("queried uncaptured/retained volume")
			}
		})
	}
}

func TestMultiDownConcurrencyDeadlineAndQueuedWorkers(t *testing.T) {
	c := newDownCloud("i-11111111", "i-22222222", "i-33333333", "i-44444444", "i-55555555", "i-66666666")
	s := downService(c)
	plan, err := s.SelectDown(context.Background(), DownSelection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	var active, maximum atomic.Int32
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	c.terminateHook = func(ctx context.Context, _ *ec2.TerminateInstancesInput) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	type finished struct {
		out TeardownOutcome
		err error
	}
	done := make(chan finished, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { out, err := s.ExecuteDown(ctx, plan); done <- finished{out, err} }()
	for n := 0; n < 4; n++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workers did not run concurrently")
		}
	}
	if maximum.Load() != 4 {
		t.Fatal("unexpected concurrency", maximum.Load())
	}
	close(release)
	got := <-done
	if got.err != nil || got.out.CleanedCount != 6 || maximum.Load() > 4 {
		t.Fatalf("queued peers lost: %+v %v maximum=%d", got.out, got.err, maximum.Load())
	}

	// One worker can wait until the shared caller deadline while its peers
	// finish. The deadline retains all selected identities and known roots.
	c = newDownCloud("i-11111111", "i-22222222", "i-33333333", "i-44444444", "i-55555555")
	s = downService(c)
	plan, _ = s.SelectDown(context.Background(), DownSelection{All: true})
	c.terminateHook = func(ctx context.Context, in *ec2.TerminateInstancesInput) error {
		if in.InstanceIds[0] == "i-11111111" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	result, err := s.ExecuteDown(short, plan)
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Workers) != 5 || result.CleanedCount != 4 || downResultWorker(t, result, "i-11111111").Status != "termination_unknown" {
		t.Fatalf("slow worker hid peers: %+v %v", result, err)
	}
}

func TestMultiDownCancellationBeforeDispatchAndConcurrentTermination(t *testing.T) {
	for _, mode := range []string{"cancel", "terminated", "shutting-down"} {
		t.Run(mode, func(t *testing.T) {
			c := newDownCloud("i-11111111")
			s := downService(c)
			plan, err := s.SelectDown(context.Background(), DownSelection{Targets: []string{"smoke"}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			} else {
				calls := 0
				c.describeHook = func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error, bool) {
					calls++
					i := worker("i-11111111")
					i.BlockDeviceMappings = nil
					i.State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
					if mode == "shutting-down" && calls == 1 {
						i.State.Name = types.InstanceStateNameShuttingDown
					}
					return inventory(i), nil, true
				}
			}
			result, err := s.ExecuteDown(ctx, plan)
			if len(c.terminated) != 0 || len(result.Workers[0].Volumes) != 1 {
				t.Fatalf("concurrent/canceled teardown mutated or lost roots: %+v %v", result, err)
			}
			if mode == "cancel" && (!errors.Is(err, context.Canceled) || result.CleanedCount != 0) || mode != "cancel" && (err != nil || result.CleanedCount != 1) {
				t.Fatalf("concurrent/canceled result: %+v %v", result, err)
			}
		})
	}
}

func TestMultiDownPartialExactReadPreservesAdditionalRootEvidence(t *testing.T) {
	c := newDownCloud("i-11111111")
	s := downService(c)
	plan, err := s.SelectDown(context.Background(), DownSelection{Targets: []string{"smoke"}})
	if err != nil {
		t.Fatal(err)
	}
	c.describeHook = func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error, bool) {
		i := worker("i-11111111")
		i.BlockDeviceMappings[0].Ebs.VolumeId = aws.String("vol-22222222")
		return inventory(i), errors.New("PRIVATE partial read failure"), true
	}
	result, err := s.ExecuteDown(context.Background(), plan)
	if err != nil || len(c.terminated) != 0 || len(result.Workers[0].Volumes) != 2 || result.CleanedCount != 0 || len(result.Errors) != 1 || strings.Contains(fmt.Sprint(result), "PRIVATE") {
		t.Fatalf("partial observation lost roots or authorized termination: %+v %v", result, err)
	}
}

func TestMultiDownRootFailureDoesNotHideOtherKnownVolumeResults(t *testing.T) {
	c := newDownCloud("i-11111111")
	i := c.workers["i-11111111"]
	i.BlockDeviceMappings = append(i.BlockDeviceMappings, types.InstanceBlockDeviceMapping{DeviceName: aws.String("/dev/sdb"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-22222222"), DeleteOnTermination: aws.Bool(true)}})
	c.workers["i-11111111"] = i
	c.volumeHook = func(_ context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
		if in.VolumeIds[0] == "vol-11111111" {
			return nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation"}
		}
		return nil, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound"}
	}
	s := downService(c)
	plan, err := s.SelectDown(context.Background(), DownSelection{Targets: []string{"smoke"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ExecuteDown(context.Background(), plan)
	if err != nil || result.CleanedCount != 0 || result.TerminatedCount != 1 || result.Workers[0].RootDeletion != "unavailable" || len(result.Workers[0].Volumes) != 2 || result.Workers[0].Volumes[1].Deletion != "deleted" {
		t.Fatalf("root failure hid peer volume observation: %+v %v", result, err)
	}
}
