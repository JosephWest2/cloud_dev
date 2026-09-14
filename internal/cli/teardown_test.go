package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type teardownCLIAPI struct {
	*ec2.Client
	mu         sync.Mutex
	workers    map[string]types.Instance
	terminated []string
}

func teardownCLIWorker(id, name, group string) types.Instance {
	return types.Instance{InstanceId: aws.String(id), State: &types.InstanceState{Name: types.InstanceStateNameRunning}, Tags: []types.Tag{
		{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String("test")},
		{Key: aws.String("Owner"), Value: aws.String("test-owner")}, {Key: aws.String("Name"), Value: aws.String(name)}, {Key: aws.String("Group"), Value: aws.String(group)},
	}, RootDeviceName: aws.String("/dev/sda1"), BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-" + strings.TrimPrefix(id, "i-")), DeleteOnTermination: aws.Bool(true)}}}}
}

func (a *teardownCLIAPI) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []types.Instance{}
	for id, worker := range a.workers {
		match := len(in.InstanceIds) == 0
		for _, wanted := range in.InstanceIds {
			match = match || wanted == id
		}
		tags := map[string]string{}
		for _, tag := range worker.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		for _, filter := range in.Filters {
			match = match && len(filter.Values) == 1 && tags[strings.TrimPrefix(aws.ToString(filter.Name), "tag:")] == filter.Values[0]
		}
		if match {
			out = append(out, worker)
		}
	}
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String("123456789012"), Instances: out}}}, nil
}
func (a *teardownCLIAPI) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range in.InstanceIds {
		a.terminated = append(a.terminated, id)
		worker := a.workers[id]
		worker.State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
		a.workers[id] = worker
	}
	return &ec2.TerminateInstancesOutput{}, nil
}
func (a *teardownCLIAPI) DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return nil, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound", Message: "SECRET removed volume"}
}

func teardownCLIFixture(t *testing.T) (string, lifecycle.Dependencies, *teardownCLIAPI) {
	t.Helper()
	api := &teardownCLIAPI{workers: map[string]types.Instance{
		"i-11111111": teardownCLIWorker("i-11111111", "first", "smoke"), "i-22222222": teardownCLIWorker("i-22222222", "second", "smoke"),
	}}
	deps := lifecycle.Dependencies{New: func(_ context.Context, c config.Config) (*lifecycle.Service, error) {
		return &lifecycle.Service{API: api, Scope: c, PollInterval: time.Millisecond}, nil
	}}
	return testutil.Setup(t), deps, api
}

func TestTeardownCLIConfirmationGatesEveryTermination(t *testing.T) {
	for _, mode := range []string{"yes", "decline", "eof", "piped-yes", "skip", "cancel", "new-worker"} {
		t.Run(mode, func(t *testing.T) {
			path, deps, api := teardownCLIFixture(t)
			var stdout, stderr bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			answer, interactive := "yes\n", true
			switch mode {
			case "decline":
				answer = "n\n"
			case "eof":
				answer = "yes"
			case "piped-yes", "skip":
				interactive = false
			}
			confirm := newDownConfirmation(strings.NewReader(answer), &stderr, func() bool { return interactive })
			deps.ConfirmDown = func(ctx context.Context, c config.Config, ids []lifecycle.Instance, yes bool) (bool, error) {
				if len(ids) != 2 {
					t.Fatalf("wrong frozen set: %+v", ids)
				}
				if mode == "cancel" {
					cancel()
					return true, nil
				}
				if mode == "new-worker" {
					api.mu.Lock()
					api.workers["i-33333333"] = teardownCLIWorker("i-33333333", "late", "smoke")
					api.mu.Unlock()
				}
				return confirm(ctx, c, ids, yes)
			}
			args := []string{"down", "--all", "--json", "--config", path}
			if mode == "skip" {
				args = append(args, "--yes")
			}
			code := RunWithLifecycle(ctx, args, &stdout, &stderr, doctor.Dependencies{}, deps)
			var result lifecycle.TeardownResult
			decoder := json.NewDecoder(&stdout)
			if err := decoder.Decode(&result); err != nil || decoder.Decode(new(any)) != io.EOF {
				t.Fatalf("polluted JSON: %v %s", err, stdout.String())
			}
			wantCode, wantTerm := 0, 2
			switch mode {
			case "decline":
				wantTerm = 0
			case "eof", "piped-yes":
				wantCode, wantTerm = 2, 0
			case "cancel":
				wantCode, wantTerm = 4, 0
			}
			if code != wantCode || len(api.terminated) != wantTerm || result.SchemaVersion != 2 || len(result.Workers) != 2 {
				t.Fatalf("confirmation escaped: mode=%s code=%d result=%+v calls=%v", mode, code, result, api.terminated)
			}
			if wantTerm == 2 {
				if result.CleanedCount != 2 || result.TerminatedCount != 2 || !result.OK {
					t.Fatalf("cleanup evidence missing: %+v", result)
				}
				sort.Strings(api.terminated)
				if !reflect.DeepEqual(api.terminated, []string{"i-11111111", "i-22222222"}) {
					t.Fatal("candidate set expanded")
				}
			}
			if mode != "cancel" && !strings.Contains(stderr.String(), "count=2") {
				t.Fatal("preview missing")
			}
			if strings.Contains(stderr.String(), "SECRET") {
				t.Fatal("raw API error exposed")
			}
		})
	}
}

func TestTeardownCLIGroupAndMixedTargetsUseFrozenScopedIDs(t *testing.T) {
	for _, mode := range []string{"group", "mixed", "dedup", "repeat"} {
		t.Run(mode, func(t *testing.T) {
			path, deps, api := teardownCLIFixture(t)
			api.workers["i-33333333"] = teardownCLIWorker("i-33333333", "foreign-group", "other")
			args := []string{"down"}
			wantCode, wantCount := 0, 1
			switch mode {
			case "group":
				args = append(args, "--group", "smoke")
				wantCount = 2
			case "mixed":
				args = append(args, "first", "missing")
				wantCode = 3
			case "dedup":
				args = append(args, "first", "i-11111111", "first")
			case "repeat":
				args = append(args, "i-11111111")
			}
			args = append(args, "--json", "--config", path)
			var stdout, stderr bytes.Buffer
			code := RunWithLifecycle(context.Background(), args, &stdout, &stderr, doctor.Dependencies{}, deps)
			var result lifecycle.TeardownResult
			if json.Unmarshal(stdout.Bytes(), &result) != nil || code != wantCode || result.CleanedCount != wantCount || len(api.terminated) != wantCount {
				t.Fatalf("wrong scoped result: %d %+v calls=%v", code, result, api.terminated)
			}
			if mode == "repeat" {
				stdout.Reset()
				if code = RunWithLifecycle(context.Background(), args, &stdout, &stderr, doctor.Dependencies{}, deps); code != 0 || len(api.terminated) != 1 {
					t.Fatal("repeated cleanup mutated again")
				}
			}
			if api.workers["i-33333333"].State.Name != types.InstanceStateNameRunning {
				t.Fatal("unselected group was touched")
			}
		})
	}
}

func TestTeardownCLIShortOutputRetainsAllCleanupIdentities(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		path, deps, api := teardownCLIFixture(t)
		var diagnostics bytes.Buffer
		deps.ConfirmDown = newDownConfirmation(nil, &diagnostics, func() bool { return false })
		args := []string{"down", "--all", "--yes", "--config", path}
		if jsonMode {
			args = append(args, "--json")
		}
		output := &downFailingOutput{failAt: 1, short: true}
		code := RunWithLifecycle(context.Background(), args, output, &diagnostics, doctor.Dependencies{}, deps)
		if code != 1 || len(api.terminated) != 2 {
			t.Fatalf("short result silently succeeded: code=%d calls=%v", code, api.terminated)
		}
		for _, id := range []string{"i-11111111", "i-22222222", "vol-11111111", "vol-22222222"} {
			if !strings.Contains(diagnostics.String(), id) {
				t.Fatalf("lost cleanup identity %s: %s", id, diagnostics.String())
			}
		}
	}
}

func TestBatchShortOutputRetainsAllAllocationIdentities(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		var diagnostics bytes.Buffer
		result := lifecycle.BatchResult{SchemaVersion: 2, Command: "up", OK: true, BatchOutcome: lifecycle.BatchOutcome{Workers: []lifecycle.WorkerOutcome{{Instance: lifecycle.Instance{ID: "i-11111111", Volumes: []lifecycle.Volume{{ID: "vol-11111111", Root: true}}}}}}}
		code := emitBatch(result, "devbox", jsonMode, &downFailingOutput{failAt: 1, short: true}, &diagnostics)
		if code != 1 || !strings.Contains(diagnostics.String(), "i-11111111") || !strings.Contains(diagnostics.String(), "vol-11111111") {
			t.Fatalf("short batch result lost identity: code=%d %s", code, diagnostics.String())
		}
	}
}
