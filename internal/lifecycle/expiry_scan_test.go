package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type scanExpiryCase struct {
	name, status, value string
	values              []*string
}

func scanExpiryCases(deadline string) []scanExpiryCase {
	future := "2026-09-14T03:00:00Z"
	return []scanExpiryCase{
		{name: "missing", status: "missing"},
		{name: "invalid", status: "invalid", values: []*string{aws.String("PRIVATE invalid")}},
		{name: "empty", status: "invalid", values: []*string{aws.String("")}},
		{name: "nil", status: "invalid", values: []*string{nil}},
		{name: "noncanonical", status: "invalid", values: []*string{aws.String("2026-09-14T02:00:00+00:00")}},
		{name: "duplicate", status: "duplicate", values: []*string{&deadline, &deadline}},
		{name: "conflicting-duplicate", status: "duplicate", values: []*string{&deadline, aws.String("PRIVATE invalid")}},
		{name: "nil-duplicate", status: "duplicate", values: []*string{nil, nil}},
		{name: "original", status: "expired", value: deadline, values: []*string{&deadline}},
		{name: "future", status: "future", value: future, values: []*string{&future}},
	}
}

func (c scanExpiryCase) apply(instance types.Instance) types.Instance {
	tags := make([]types.Tag, 0, len(instance.Tags))
	for _, tag := range instance.Tags {
		if aws.ToString(tag.Key) != "ExpiresAt" {
			tags = append(tags, tag)
		}
	}
	for _, value := range c.values {
		tags = append(tags, types.Tag{Key: aws.String("ExpiresAt"), Value: value})
	}
	instance.Tags = tags
	return instance
}

// Inspect the public JSON as well as retained IDs: absent/invalid expiry must
// remain null, and observation diagnostics must not replace the request deadline.
func assertScanExpiryResult(t *testing.T, result *BatchResult, plan LaunchPlan, id string, want scanExpiryCase, fulfilled int) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var public struct {
		ExpiresAt string `json:"expires_at"`
		Instances []struct {
			ID        string  `json:"instance_id"`
			ExpiresAt *string `json:"expires_at"`
			Status    string  `json:"expiry_status"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(raw, &public); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Plan, plan) || result.Plan.Digest() != plan.Digest() || public.ExpiresAt != plan.ExpiresAt || result.FulfilledCount != fulfilled || result.Attempts[0].FulfilledCount != fulfilled || len(public.Instances) != fulfilled {
		t.Fatalf("request deadline, identity or historical fulfillment changed: %s", raw)
	}
	found := false
	for _, worker := range public.Instances {
		if worker.ID != id {
			continue
		}
		found = true
		if worker.Status != want.status || (want.value == "" && worker.ExpiresAt != nil) || (want.value != "" && (worker.ExpiresAt == nil || *worker.ExpiresAt != want.value)) {
			t.Fatalf("want observed %s/%s; public result: %s", want.status, want.value, raw)
		}
	}
	if !found || bytes.Contains(raw, []byte("PRIVATE")) {
		t.Fatalf("identity missing or raw tag leaked: %s", raw)
	}
}

func TestExpiryScanFallbackAndExactPrecedence(t *testing.T) {
	for _, state := range []types.InstanceStateName{types.InstanceStateNameRunning, types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated} {
		for _, scan := range scanExpiryCases("2026-09-14T02:00:00Z") {
			// A row in a partial response is newer evidence too. No-row responses
			// (including successful empty inventory) must retain the scan evidence.
			for _, exact := range []string{"unavailable", "not-found", "empty", "original", "missing", "partial-original", "partial-missing"} {
				t.Run(fmt.Sprintf("%s/%s/%s", state, scan.name, exact), func(t *testing.T) {
					snapshot, api := reconcileFixture(t, "complete", 1, 1)
					before, err := json.Marshal(snapshot)
					if err != nil {
						t.Fatal(err)
					}
					plan := snapshot.Receipt.Plan
					id := snapshot.Receipt.Attempts[0].InstanceIDs[0]
					item := api.instances[id]
					item.State = &types.InstanceState{Name: state}
					page := func(row types.Instance) *ec2.DescribeInstancesOutput {
						return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(api.account), Instances: []types.Instance{row}}}}
					}
					api.scanPages = []fleetInstancesPage{{out: page(scan.apply(item))}}
					want := scan
					switch exact {
					case "unavailable":
						api.exactErrors[id] = errors.New("controlled exact read failure")
					case "not-found":
						api.exactErrors[id] = &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
					case "empty":
						delete(api.instances, id)
					default:
						want = scanExpiryCase{name: "original", status: "expired", value: plan.ExpiresAt, values: []*string{&plan.ExpiresAt}}
						if exact == "missing" || exact == "partial-missing" {
							want = scanExpiryCase{name: "missing", status: "missing"}
						}
						response := fleetInstancesPage{out: page(want.apply(item))}
						if exact == "partial-original" || exact == "partial-missing" {
							response.err = errors.New("controlled partial exact read")
						}
						api.exactPages = map[string][]fleetInstancesPage{id: {response}}
					}
					clk := fixedClock{expiryTime(t, plan.ExpiresAt)}
					observed, err := ReconcileLaunch(context.Background(), api, snapshot, clk)
					outcome := (&RecoveryService{Clock: clk}).outcome(observed)
					result := &BatchResult{BatchOutcome: outcome}
					assertScanExpiryResult(t, result, plan, id, want, 1)
					// Only fully matching scans with a complete valid exact read (or an
					// already fulfilled ID now absent) retain the original capacity bound.
					bounded := scan.name == "original" && (exact == "not-found" || exact == "empty" || exact == "original")
					if bounded {
						if err != nil || outcome.MissingCount == nil || *outcome.MissingCount != 1 {
							t.Fatalf("valid control lost bounds: %+v %v", outcome, err)
						}
					} else if err == nil || outcome.MissingCount != nil {
						t.Fatalf("incomplete/mismatched observation granted bounds: %+v %v", outcome, err)
					}
					after, err := json.Marshal(snapshot)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("observation rewrote immutable snapshot")
					}
				})
			}
		}
	}
}

// Cancel only after recovery's exact-ID inspection has finished and startup
// retries it. This exercises readiness's fallback without timing-dependent waits.
type scanExpiryUnavailableInventory struct {
	*batchRunEC2
	target string
	err    error
	cancel context.CancelFunc
	mu     sync.Mutex
	calls  int
}

func (a *scanExpiryUnavailableInventory) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if len(in.InstanceIds) == 1 && in.InstanceIds[0] == a.target {
		a.mu.Lock()
		a.calls++
		if a.calls == 2 {
			a.cancel()
		}
		a.mu.Unlock()
		return nil, a.err
	}
	return a.batchRunEC2.DescribeInstances(ctx, in, opts...)
}

func TestExpiryScanPublicResumeAfterCacheLoss(t *testing.T) {
	for _, state := range []types.InstanceStateName{types.InstanceStateNameRunning, types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated} {
		for _, scan := range scanExpiryCases("2026-09-14T02:00:00Z") {
			for _, gone := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/not-found=%t", state, scan.name, gone), func(t *testing.T) {
					path, deps, api, _, selection := batchRunFixture(t)
					deps.Clock = fixedClock{expiryTime(t, "2026-09-14T00:00:00Z")}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					first := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
					if first == nil || !first.OK || len(first.Workers) != 2 {
						t.Fatalf("initial launch: %+v", first)
					}
					id := first.Workers[0].ID
					deps.Clock = fixedClock{expiryTime(t, first.Plan.ExpiresAt)}
					api.mu.Lock()
					for workerID, item := range api.instances {
						item.State = &types.InstanceState{Name: state}
						if workerID == id {
							item = scan.apply(item)
						}
						api.instances[workerID] = item
					}
					api.mu.Unlock()
					cfg, err := config.Load(path, config.Overrides{})
					if err != nil {
						t.Fatal(err)
					}
					factory := deps.New
					service, err := factory(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					ledger := service.LaunchRecords.(*launchMemoryS3)
					before, err := json.Marshal(ledger.objects)
					if err != nil {
						t.Fatal(err)
					}
					readErr := error(errors.New("controlled exact read failure"))
					if gone {
						readErr = &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
					}
					inventory := &scanExpiryUnavailableInventory{batchRunEC2: api, target: id, err: readErr, cancel: cancel}
					deps.New = func(ctx context.Context, c config.Config) (*Service, error) {
						svc, err := factory(ctx, c)
						if err == nil {
							svc.API = inventory
						}
						return svc, err
					}
					deps.Store = &Store{Dir: t.TempDir()}
					result := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &LaunchSelection{Resume: first.RequestID}}, deps, io.Discard).Batch
					if result == nil {
						t.Fatal("missing public result")
					}
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						t.Fatal("recovery exceeded test deadline")
					}
					assertScanExpiryResult(t, result, first.Plan, id, scan, 2)
					if gone && scan.name == "original" {
						if result.MissingCount == nil || *result.MissingCount != 0 {
							t.Fatal("valid historic recovery lost bounds")
						}
					} else if result.MissingCount != nil {
						t.Fatal("failed or mismatched observation granted allocation bounds")
					}
					if len(api.counts) != 1 {
						t.Fatal("recovery allocated new workers", api.counts)
					}
					if len(result.Workers) != 2 || result.Workers[1].ID != first.Workers[1].ID {
						t.Fatal("peer recovery identity lost")
					}
					after, err := json.Marshal(ledger.objects)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("resume rewrote permanent ledger records")
					}
				})
			}
		}
	}
}
