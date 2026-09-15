package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// Script each public recovery phase independently. The initial exact read fails;
// startup, terminal resolution, or the final pre-probe read supplies newer tags;
// then readiness triggers another complete shared-ledger reconciliation.
type expiryRefreshInventory struct {
	*batchRunEC2
	mu2                       sync.Mutex
	target, phase, refresh    string
	scans, exact              int
	latest, scanNew, exactNew scanExpiryCase
}

func (a *expiryRefreshInventory) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	a.mu2.Lock()
	defer a.mu2.Unlock()
	if len(in.InstanceIds) == 0 {
		a.scans++
		if a.scans > 1 && a.refresh == "scan-error" {
			return nil, errors.New("controlled refresh scan failure")
		}
		out, err := a.batchRunEC2.DescribeInstances(ctx, in, opts...)
		if out != nil {
			for n := range out.Reservations {
				kept := []types.Instance{}
				for _, row := range out.Reservations[n].Instances {
					if aws.ToString(row.InstanceId) == a.target {
						if a.scans == 1 {
							row = (scanExpiryCase{name: "missing"}).apply(row)
						} else {
							if a.refresh != "scan-new" && a.refresh != "partial-scan" && a.refresh != "both-new" {
								continue
							}
							row = a.scanNew.apply(row)
							row.State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
						}
					}
					kept = append(kept, row)
				}
				out.Reservations[n].Instances = kept
			}
		}
		if a.scans > 1 && a.refresh == "partial-scan" {
			err = errors.New("controlled partial refresh scan")
		}
		return out, err
	}
	if len(in.InstanceIds) != 1 || in.InstanceIds[0] != a.target {
		return a.batchRunEC2.DescribeInstances(ctx, in, opts...)
	}
	a.exact++
	if a.exact == 1 {
		return nil, errors.New("controlled initial exact failure")
	}
	phaseRead := map[string]int{"startup": 2, "resolve": 3, "probe": 4, "resolve-running": 3, "probe-running": 4}[a.phase]
	if a.scans > 1 {
		switch a.refresh {
		case "empty":
			return &ec2.DescribeInstancesOutput{}, nil
		case "exact-error":
			return nil, errors.New("controlled refresh exact failure")
		case "exact-new", "partial-exact", "both-new":
			out, err := a.batchRunEC2.DescribeInstances(ctx, in, opts...)
			a.rewrite(out, a.exactNew, true)
			if a.refresh == "partial-exact" {
				err = errors.New("controlled partial refresh exact response")
			}
			return out, err
		default:
			return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
		}
	}
	if a.exact > phaseRead {
		// Resolve filters the terminal row, then its terminal fallback has no row.
		return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
	}
	out, err := a.batchRunEC2.DescribeInstances(ctx, in, opts...)
	if a.exact == phaseRead {
		a.rewrite(out, a.latest, a.phase != "resolve-running" && a.phase != "probe-running")
		if a.refresh == "latest-partial" {
			err = errors.New("controlled partial readiness observation")
		}
	}
	return out, err
}

func (a *expiryRefreshInventory) rewrite(out *ec2.DescribeInstancesOutput, tags scanExpiryCase, terminal bool) {
	if out == nil {
		return
	}
	for n := range out.Reservations {
		for j, row := range out.Reservations[n].Instances {
			row = tags.apply(row)
			if terminal {
				row.State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
			}
			out.Reservations[n].Instances[j] = row
		}
	}
}

func TestExpiryPublicReadinessRefreshProvenance(t *testing.T) {
	cases := scanExpiryCases("2026-09-14T02:00:00Z")
	for _, phase := range []string{"startup", "resolve", "probe", "resolve-running", "probe-running"} {
		for n, latest := range cases {
			for _, refresh := range []string{"not-found", "empty", "exact-error", "scan-error", "scan-new", "partial-scan", "exact-new", "partial-exact", "both-new", "latest-partial"} {
				t.Run(fmt.Sprintf("%s/%s/%s", phase, latest.name, refresh), func(t *testing.T) {
					path, deps, api, _, selection := batchRunFixture(t)
					deps.Clock = fixedClock{expiryTime(t, "2026-09-14T00:00:00Z")}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					first := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
					if first == nil || !first.OK {
						t.Fatalf("initial launch: %+v", first)
					}
					cfg, err := config.Load(path, config.Overrides{})
					if err != nil {
						t.Fatal(err)
					}
					factory := deps.New
					svc, err := factory(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					ledger := svc.LaunchRecords.(*launchMemoryS3)
					before, err := json.Marshal(ledger.objects)
					if err != nil {
						t.Fatal(err)
					}
					a := &expiryRefreshInventory{batchRunEC2: api, target: first.Workers[0].ID, phase: phase, refresh: refresh, latest: latest, scanNew: cases[(n+1)%len(cases)], exactNew: cases[(n+2)%len(cases)]}
					deps.New = func(ctx context.Context, c config.Config) (*Service, error) {
						s, err := factory(ctx, c)
						if err == nil {
							s.API = a
						}
						return s, err
					}
					deps.Clock = fixedClock{expiryTime(t, first.Plan.ExpiresAt)}
					deps.Store = &Store{Dir: t.TempDir()}
					result := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &LaunchSelection{Resume: first.RequestID}}, deps, io.Discard).Batch
					if ctx.Err() != nil {
						t.Fatal("recovery timed out", ctx.Err())
					}
					if result == nil {
						t.Fatal("missing result")
					}
					want := latest
					switch refresh {
					case "scan-new", "partial-scan":
						want = a.scanNew
					case "exact-new", "partial-exact", "both-new":
						want = a.exactNew
					}
					assertScanExpiryResult(t, result, first.Plan, a.target, want, 2)
					if a.scans != 2 || a.exact < 3 {
						t.Fatalf("refresh not exercised: scans=%d exact=%d", a.scans, a.exact)
					}
					if result.MissingCount != nil || len(api.counts) != 1 || len(result.Workers) != 2 || result.Workers[1].ID != first.Workers[1].ID {
						t.Fatal("allocation bounds or stable peer changed")
					}
					after, err := json.Marshal(ledger.objects)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("refresh rewrote permanent records")
					}
				})
			}
		}
	}
}

func TestExpiryProvenanceIsInMemoryOnly(t *testing.T) {
	snapshot, api := reconcileFixture(t, "complete", 1, 1)
	plan := snapshot.Receipt.Plan
	id := snapshot.Receipt.Attempts[0].InstanceIDs[0]
	clk := fixedClock{expiryTime(t, plan.ExpiresAt)}
	// With no observed row, the original deadline remains the public fallback.
	api.scanPages = []fleetInstancesPage{{out: &ec2.DescribeInstancesOutput{}}}
	api.exactErrors[id] = &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
	observed, err := ReconcileLaunch(context.Background(), api, snapshot, clk)
	if err != nil {
		t.Fatal(err)
	}
	current := (&RecoveryService{Clock: clk}).outcome(observed)
	if current.Workers[0].expiryObserved || current.Workers[0].ExpiresAt != plan.ExpiresAt {
		t.Fatal("unobserved deadline became AWS evidence")
	}
	for _, tags := range scanExpiryCases(plan.ExpiresAt) {
		t.Run(tags.name, func(t *testing.T) {
			previous := current
			previous.Workers = cloneWorkers(current.Workers)
			row := tags.apply(api.instances[id])
			inspectInstanceExpiry(&previous.Workers[0].Instance, row.Tags, clockNow(clk))
			copied := cloneWorkers(previous.Workers)
			if !copied[0].expiryObserved {
				t.Fatal("outcome clone lost provenance")
			}
			raw, err := json.Marshal(copied)
			if err != nil {
				t.Fatal(err)
			}
			var decoded []WorkerOutcome
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded[0].expiryObserved {
				t.Fatal("serialized worker asserted observation provenance")
			}
			again, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, again) {
				t.Fatal("worker wire encoding changed")
			}
			next := current
			next.Workers = cloneWorkers(current.Workers)
			retainBatchExpiry(&next, previous)
			if !next.Workers[0].expiryObserved || next.Workers[0].ExpiresAt != tags.value || next.Workers[0].ExpiryStatus != tags.status {
				t.Fatal("observation lost through refresh")
			}
			// Carry the evidence through another empty refresh, and reject a different
			// request's fallback. Diagnostic provenance confers no cross-plan authority.
			newer := current
			newer.Workers = cloneWorkers(current.Workers)
			retainBatchExpiry(&newer, next)
			if !newer.Workers[0].expiryObserved {
				t.Fatal("second refresh lost provenance")
			}
			newer.Workers = cloneWorkers(current.Workers)
			next.Plan = cloneBatch(BatchReceipt{Plan: plan}).Plan
			next.Plan.BaseName = "different"
			retainBatchExpiry(&newer, next)
			if newer.Workers[0].expiryObserved {
				t.Fatal("different plan supplied fallback")
			}
		})
	}
}

// A concurrent preparation conflict observes the winner through Resume, which
// must retain the prior valid observation when the second read has no row.
type expiryConflictLedger struct {
	BatchLedger
	prepares int
}

func (l *expiryConflictLedger) Prepare(context.Context, BatchReceipt, PreparedAttempt) error {
	l.prepares++
	return ErrLaunchRecordConflict
}

func TestExpiryRetryConflictRefreshProvenance(t *testing.T) {
	for _, newRow := range []bool{false, true} {
		t.Run(fmt.Sprintf("new-row=%t", newRow), func(t *testing.T) {
			s, storage, m, p, r, fleet := recoveryFixture(t, "i-12345678")
			s.Clock = fixedClock{expiryTime(t, r.Plan.CreatedAt)}
			s.Dispatcher.Clock = s.Clock
			// Isolate the losing candidate's local cache from the recovery client;
			// a local successor absent from shared records is a separate conflict.
			s.Dispatcher.Cache = &Store{Dir: t.TempDir()}
			conflict := &expiryConflictLedger{BatchLedger: s.Dispatcher.Ledger}
			s.Dispatcher.Ledger = conflict
			observer := s.Observe
			reads := 0
			s.Observe = func(ctx context.Context, snapshot LaunchSnapshot) (LaunchObservation, error) {
				result, err := observer(ctx, snapshot)
				reads++
				if reads == 1 {
					inspectInstanceExpiry(&result.Workers[0].Instance, []types.Tag{{Key: aws.String("ExpiresAt"), Value: aws.String(r.Plan.ExpiresAt)}}, clockNow(s.Clock))
				} else if newRow {
					inspectInstanceExpiry(&result.Workers[0].Instance, nil, clockNow(s.Clock))
					result.Bounded = false
				}
				return result, err
			}
			before, err := json.Marshal(storage.objects)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(BatchReceipt, string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if reads != 2 || conflict.prepares != 1 || len(fleet.counts) != 0 || len(result.Workers) != 1 || result.Workers[0].ID != "i-12345678" || result.FulfilledCount != 1 || result.Plan.Digest() != r.Plan.Digest() {
				t.Fatalf("unexpected conflict recovery: %+v reads=%d prepares=%d", result, reads, conflict.prepares)
			}
			worker := result.Workers[0]
			if !worker.expiryObserved {
				t.Fatal("retry conflict discarded prior observation provenance")
			}
			if newRow {
				if worker.ExpiryStatus != "missing" || worker.ExpiresAt != "" || result.MissingCount != nil {
					t.Fatal("new observation or allocation bounds lost")
				}
			} else if worker.ExpiresAt != r.Plan.ExpiresAt || worker.ExpiryStatus != "future" || result.MissingCount == nil || *result.MissingCount != 1 {
				t.Fatal("unobserved refresh changed valid evidence")
			}
			after, err := json.Marshal(storage.objects)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("conflict recovery rewrote permanent records")
			}
		})
	}
}

type expiryRepeatedRowInventory struct {
	*batchRunEC2
	row   types.Instance
	later scanExpiryCase
}

func (a *expiryRepeatedRowInventory) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String("123456789012"), Instances: []types.Instance{a.row, a.later.apply(a.row)}}}}, nil
}

func TestExpiryInventoryRetainsLatestConflictingRow(t *testing.T) {
	path, deps, api, _, selection := batchRunFixture(t)
	deps.Clock = fixedClock{expiryTime(t, "2026-09-14T00:00:00Z")}
	first := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
	if first == nil || !first.OK {
		t.Fatalf("initial launch: %+v", first)
	}
	cfg, err := config.Load(path, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := deps.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := first.Workers[0].ID
	for _, later := range scanExpiryCases(first.Plan.ExpiresAt) {
		t.Run(later.name, func(t *testing.T) {
			service.API = &expiryRepeatedRowInventory{batchRunEC2: api, row: api.instances[id], later: later}
			service.Clock = fixedClock{expiryTime(t, first.Plan.ExpiresAt)}
			diagnostics := first.Workers[0].Instance
			_, err := service.resolveWithExpiry(context.Background(), id, &diagnostics)
			if later.name != "original" {
				requireCode(t, err, "inventory_invalid")
			} else if err != nil {
				t.Fatal(err)
			}
			if !diagnostics.expiryObserved || diagnostics.ExpiresAt != later.value || diagnostics.ExpiryStatus != later.status {
				t.Fatalf("duplicate merge discarded newer diagnostic: %+v", diagnostics)
			}
		})
	}
}
