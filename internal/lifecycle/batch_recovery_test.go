package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type recoveryFleetAPI struct {
	mu     sync.Mutex
	counts []int
	fail   bool
}

func (a *recoveryFleetAPI) CreateFleet(_ context.Context, in *ec2.CreateFleetInput, _ ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := int(aws.ToInt32(in.TargetCapacitySpecification.TotalTargetCapacity))
	a.counts = append(a.counts, count)
	if a.fail {
		return nil, errors.New("connection lost after simulated acceptance")
	}
	choice := in.LaunchTemplateConfigs[0].Overrides[0]
	ids := []string{}
	for n := 0; n < count; n++ {
		ids = append(ids, fmt.Sprintf("i-%017x", 1000+100*len(a.counts)+n))
	}
	return &ec2.CreateFleetOutput{FleetId: aws.String(fmt.Sprintf("fleet-00000000-0000-0000-0000-%012d", len(a.counts))), Errors: []types.CreateFleetError{},
		Instances: []types.CreateFleetInstance{{InstanceIds: ids, InstanceType: choice.InstanceType, SubnetId: choice.SubnetId, AvailabilityZone: choice.AvailabilityZone, Lifecycle: types.InstanceLifecycle(in.TargetCapacitySpecification.DefaultTargetCapacityType)}}}, nil
}

// This fixture exercises the real shared S3 state machine and dispatcher. AWS
// inspection is isolated here; independent reconciliation tests cover its reads.
func recoveryFixture(t *testing.T, successful ...string) (*RecoveryService, *launchMemoryS3, config.Manifest, config.Profile, BatchReceipt, *recoveryFleetAPI) {
	t.Helper()
	l, api, c, r, p := ledgerFixture(t)
	r = ledgerPublishResponse(t, l, r, p, successful...)
	_, m, profile, _ := batchFixture(t)
	fleet := &recoveryFleetAPI{}
	s := newRecoveryFixtureService(t, l, c, fleet)
	return s, api, m, profile, r, fleet
}
func newRecoveryFixtureService(t *testing.T, l RecoveryLedger, c config.Config, fleet *recoveryFleetAPI) *RecoveryService {
	t.Helper()
	cache := Store{Dir: t.TempDir()}
	verify := func(_ context.Context, _ LaunchPlan, _ AttemptReceipt, workers []WorkerOutcome) ([]WorkerOutcome, error) {
		workers = cloneWorkers(workers)
		for n := range workers {
			workers[n].Status = "allocated"
		}
		return workers, nil
	}
	s := &RecoveryService{Scope: c, Ledger: l, Cache: cache, CommandPrefix: "devbox --config /tmp/original-config --aws-profile restricted"}
	s.Observe = func(_ context.Context, snapshot LaunchSnapshot) (LaunchObservation, error) {
		out := LaunchObservation{Receipt: cloneBatch(snapshot.Receipt), Workers: snapshotWorkers(snapshot), Errors: []ResourceError{}, Bounded: true}
		for _, a := range out.Receipt.Attempts {
			response, ok := snapshot.Responses[a.AttemptID]
			if !ok || (response.Attempt.State != "complete" && response.Attempt.State != "rejected") {
				out.Bounded = false
			}
		}
		for n := range out.Workers {
			out.Workers[n].Status = "historical"
		}
		return out, nil
	}
	s.Dispatcher = &AttemptService{API: fleet, Scope: c, Ledger: l, Cache: cache, VerifyFoundation: func(context.Context, config.Manifest, config.Profile) error { return nil }, VerifyWorkers: verify}
	return s
}

func TestRecoveryResumeNeverAllocatesAndRetryRequestsOnlyMissing(t *testing.T) {
	s, _, m, p, r, fleet := recoveryFixture(t, "i-12345678")
	id, after := r.RequestID, r.Attempts[0].AttemptID
	for n := 0; n < 2; n++ {
		out, err := s.Resume(context.Background(), id)
		if err != nil || out.FulfilledCount != 1 || out.MissingCount == nil || *out.MissingCount != 1 || len(fleet.counts) != 0 || !strings.Contains(out.ResumeCommand, s.CommandPrefix) {
			t.Fatalf("unsafe resume: %+v %v counts=%v", out, err, fleet.counts)
		}
	}
	out, err := s.RetryMissing(context.Background(), m, p, id, after, func(BatchReceipt, string) error { return nil })
	if err != nil || !reflect.DeepEqual(fleet.counts, []int{1}) || out.FulfilledCount != 2 || out.MissingCount == nil || *out.MissingCount != 0 || len(out.Attempts) != 2 {
		t.Fatalf("wrong missing retry: %+v %v counts=%v", out, err, fleet.counts)
	}
	for _, parent := range []string{after, out.Attempts[1].AttemptID, after} {
		out, err = s.RetryMissing(context.Background(), m, p, id, parent, func(BatchReceipt, string) error { t.Fatal("repeat announced new allocation"); return nil })
		if err != nil || len(fleet.counts) != 1 || out.FulfilledCount != 2 {
			t.Fatalf("repeat allocated: %+v %v", out, err)
		}
	}
}

func TestRecoveryUnknownCannotAuthorizeAnotherAttempt(t *testing.T) {
	s, api, m, p, r, fleet := recoveryFixture(t, "i-12345678")
	l := s.Ledger.(*S3LaunchLedger)
	api.mu.Lock()
	delete(api.objects, l.key(r.RequestID, r.Attempts[0].AttemptID, "response"))
	api.mu.Unlock()
	unlock, err := s.Cache.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	r.Attempts[0].State = "unknown"
	if err = s.Cache.SaveBatch(r); err != nil {
		t.Fatal(err)
	}
	unlock()
	out, err := s.Resume(context.Background(), r.RequestID)
	if err != nil || out.MissingCount != nil || len(out.Workers) != 1 || out.RetryCommand != "" {
		t.Fatalf("unknown became missing: %+v %v", out, err)
	}
	out, err = s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(BatchReceipt, string) error { t.Fatal("unknown announced allocation"); return nil })
	if err == nil || len(fleet.counts) != 0 || out.MissingCount != nil {
		t.Fatal("unknown retry dispatched")
	}
}

func TestRecoveryPreparedAndClaimedCrashesAreObservationOnly(t *testing.T) {
	for _, stage := range []string{"plan-only", "prepared", "claimed"} {
		t.Run(stage, func(t *testing.T) {
			l, api, c, r, prepared := ledgerFixture(t)
			if err := l.Prepare(context.Background(), r, prepared); err != nil {
				t.Fatal(err)
			}
			if stage == "claimed" {
				if won, err := l.Claim(context.Background(), r.Plan.LaunchLedger, expectedClaim(prepared)); err != nil || !won {
					t.Fatal(err)
				}
			}
			if stage == "plan-only" {
				api.mu.Lock()
				delete(api.objects, l.key(r.RequestID, prepared.Attempt.AttemptID, "prepared"))
				api.mu.Unlock()
			}
			fleet := &recoveryFleetAPI{}
			s := newRecoveryFixtureService(t, l, c, fleet)
			out, err := s.Resume(context.Background(), r.RequestID)
			if err != nil || len(fleet.counts) != 0 || out.MissingCount != nil || out.RetryCommand != "" {
				t.Fatalf("crash resumed into allocation: %+v %v", out, err)
			}
			want := "prepared"
			if stage == "claimed" {
				want = "allocation_unknown"
			}
			if out.Status != want {
				t.Fatalf("status=%s want=%s", out.Status, want)
			}
		})
	}
}

func TestRecoveryRestoresLostAndCorruptCachesFromSharedAuthority(t *testing.T) {
	for _, kind := range []string{"lost", "corrupt", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, _, r, fleet := recoveryFixture(t, "i-12345678")
			before := ""
			if kind != "lost" {
				before = "{broken"
				if kind == "unsupported" {
					before = `{"schema_version":99}`
				}
				if err := os.WriteFile(s.Cache.Path(r.RequestID), []byte(before), 0644); err != nil {
					t.Fatal(err)
				}
			}
			out, err := s.Resume(context.Background(), r.RequestID)
			if err != nil || len(fleet.counts) != 0 || out.FulfilledCount != 1 {
				t.Fatalf("recovery failed: %+v %v", out, err)
			}
			if restored, err := s.Cache.LoadBatch(r.RequestID); err != nil || restored.PlanSHA256 != r.PlanSHA256 {
				t.Fatal("shared cache not restored", err)
			}
			if before != "" {
				backups, _ := filepath.Glob(filepath.Join(s.Cache.Dir, r.RequestID+".corrupt-*"))
				if len(backups) != 1 {
					t.Fatal("original corrupt cache was not preserved")
				}
				b, _ := os.ReadFile(backups[0])
				info, _ := os.Stat(backups[0])
				if string(b) != before || info.Mode().Perm() != 0600 {
					t.Fatal("backup changed or is not private")
				}
			}
		})
	}
}

func TestRecoveryCopiedClientsCannotRetryTheSameRemainderTwice(t *testing.T) {
	first, storage, m, p, r, fleet := recoveryFixture(t, "i-12345678")
	secondLedger, err := NewS3LaunchLedger(storage, first.Scope, r.Plan.LaunchLedger)
	if err != nil {
		t.Fatal(err)
	}
	second := newRecoveryFixtureService(t, secondLedger, first.Scope, fleet)
	// Both clients finish reading the same one-of-two response before either
	// prepares its successor, using independent local directories and clients.
	var barrier sync.WaitGroup
	barrier.Add(2)
	for _, service := range []*RecoveryService{first, second} {
		original := service.Observe
		service.Observe = func(ctx context.Context, snapshot LaunchSnapshot) (LaunchObservation, error) {
			out, err := original(ctx, snapshot)
			if len(snapshot.Receipt.Attempts) == 1 {
				barrier.Done()
				barrier.Wait()
			}
			return out, err
		}
	}
	results := make(chan error, 2)
	for _, service := range []*RecoveryService{first, second} {
		go func(s *RecoveryService) {
			_, err := s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(BatchReceipt, string) error { return nil })
			results <- err
		}(service)
	}
	for n := 0; n < 2; n++ {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(fleet.counts, []int{1}) {
		t.Fatalf("duplicated remainder: %v", fleet.counts)
	}
	out, err := second.Resume(context.Background(), r.RequestID)
	if err != nil || out.FulfilledCount != 2 || out.MissingCount == nil || *out.MissingCount != 0 {
		t.Fatalf("copied client failed to observe winner: %+v %v", out, err)
	}
}

func TestRecoveryRetryRejectsChangedPinsScopeCapAndUnknownParent(t *testing.T) {
	for _, change := range []string{"profile", "template", "scope", "cap", "parent"} {
		t.Run(change, func(t *testing.T) {
			s, _, m, p, r, fleet := recoveryFixture(t, "i-12345678")
			if _, err := s.Resume(context.Background(), r.RequestID); err != nil {
				t.Fatal(err)
			}
			parent := r.Attempts[0].AttemptID
			switch change {
			case "profile":
				p.DiskGB++
			case "template":
				image := m.Images["agent"]
				image.LaunchTemplateVersion = "99"
				m.Images["agent"] = image
			case "scope":
				s.Scope.Owner = "different"
			case "cap":
				s.Scope.MaxCount = 1
			case "parent":
				parent = strings.Repeat("f", 32)
			}
			out, err := s.RetryMissing(context.Background(), m, p, r.RequestID, parent, func(BatchReceipt, string) error { return nil })
			if err == nil || len(fleet.counts) != 0 {
				t.Fatalf("changed request allocated: %+v %v", out, err)
			}
			if change == "cap" {
				out, err = s.Resume(context.Background(), r.RequestID)
				if err != nil || out.FulfilledCount != 1 {
					t.Fatal("lowering cap blocked observation")
				}
			}
		})
	}
}

func TestRecoveryAmbiguousSuccessorRemainsUnknownOnRepeatedRetry(t *testing.T) {
	s, _, m, p, r, fleet := recoveryFixture(t, "i-12345678")
	fleet.fail = true
	out, err := s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(BatchReceipt, string) error { return nil })
	if err == nil || len(fleet.counts) != 1 || out.MissingCount != nil || out.FulfilledCount != 1 {
		t.Fatalf("uncertainty lost: %+v %v", out, err)
	}
	for _, parent := range []string{r.Attempts[0].AttemptID, out.Attempts[1].AttemptID} {
		_, _ = s.RetryMissing(context.Background(), m, p, r.RequestID, parent, func(BatchReceipt, string) error { t.Fatal("uncertain successor redispatched"); return nil })
	}
	if !reflect.DeepEqual(fleet.counts, []int{1}) {
		t.Fatal("uncertain retry allocated twice")
	}
}

func TestRecoveryConflictingLocalIDsCannotDisappearFromOutput(t *testing.T) {
	s, _, m, p, r, fleet := recoveryFixture(t, "i-12345678")
	cached := cloneBatch(r)
	cached.Attempts[0].State = "unknown"
	cached.Attempts[0].InstanceIDs = append(cached.Attempts[0].InstanceIDs, "i-23456789")
	unlock, err := s.Cache.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Cache.SaveBatch(cached); err != nil {
		t.Fatal(err)
	}
	unlock()
	out, err := s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(BatchReceipt, string) error { return nil })
	if err == nil || len(fleet.counts) != 0 || len(out.Workers) != 2 || out.MissingCount != nil {
		t.Fatalf("conflicting IDs became permission: %+v %v", out, err)
	}
}

func TestRecoverySharedLossRetainsLocalIDsWithoutAuthority(t *testing.T) {
	s, api, m, p, r, fleet := recoveryFixture(t, "i-12345678")
	unlock, err := s.Cache.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Cache.SaveBatch(r); err != nil {
		t.Fatal(err)
	}
	unlock()
	api.mu.Lock()
	api.objects = map[string][]byte{}
	api.mu.Unlock()
	out, err := s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(BatchReceipt, string) error { return nil })
	if err == nil || len(fleet.counts) != 0 || len(out.Workers) != 1 || out.Workers[0].ID != "i-12345678" || out.MissingCount != nil || out.FulfilledCount != 0 {
		t.Fatalf("lost shared authority became retry: %+v %v", out, err)
	}
}

func TestRecoveryUnverifiedCachesExposeOnlyIDsAndFixedDiagnostics(t *testing.T) {
	for _, failureMode := range []string{"shared-loss", "scope-mismatch", "plan-conflict"} {
		t.Run(failureMode, func(t *testing.T) {
			s, api, _, _, shared, fleet := recoveryFixture(t, "i-12345678")
			cached := cloneBatch(shared)
			cached.Attempts[0].Errors = []ResourceError{{Code: "PRIVATE-code", Message: "PRIVATE cached payload"}}
			if failureMode == "plan-conflict" {
				cached.Plan.BaseName = "different"
				cached.PlanSHA256 = cached.Plan.Digest()
				cached.Attempts[0].ClientToken = attemptToken(cached.PlanSHA256, cached.Attempts[0].AttemptID, cached.Attempts[0].RequestedCount)
				cached.Attempts[0].InstanceIDs = []string{"i-23456789"}
			}
			unlock, err := s.Cache.Lock(context.Background(), cached.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			err = s.Cache.SaveBatch(cached)
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			switch failureMode {
			case "shared-loss":
				api.mu.Lock()
				api.objects = map[string][]byte{}
				api.mu.Unlock()
			case "scope-mismatch":
				s.Scope.Owner = "different"
			}
			out, err := s.Resume(context.Background(), cached.RequestID)
			if err == nil || out.FulfilledCount != 0 || out.MissingCount != nil || out.Status != "allocation_unknown" || out.RetryCommand != "" || len(fleet.counts) != 0 {
				t.Fatalf("local evidence gained authority: %+v %v", out, err)
			}
			encoded, marshalErr := json.Marshal(out)
			if marshalErr != nil || strings.Contains(string(encoded), "PRIVATE") {
				t.Fatalf("untrusted diagnostics escaped: %s %v", encoded, marshalErr)
			}
			wantIDs := []string{"i-12345678"}
			if failureMode == "plan-conflict" {
				wantIDs = append(wantIDs, "i-23456789")
			}
			gotIDs := []string{}
			for _, w := range out.Workers {
				gotIDs = append(gotIDs, w.ID)
				if w.Status != "not_observed" {
					t.Fatal("cached worker presented as verified")
				}
			}
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("lost known evidence: got %v want %v", gotIDs, wantIDs)
			}
		})
	}
}
