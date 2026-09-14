package lifecycle

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type attemptCacheFake struct {
	Store
	events          *[]string
	saves, failSave int
	lockErr         error
}

func (s *attemptCacheFake) Lock(ctx context.Context, id string) (func(), error) {
	*s.events = append(*s.events, "lock")
	if s.lockErr != nil {
		return nil, s.lockErr
	}
	unlock, err := s.Store.Lock(ctx, id)
	return func() {
		*s.events = append(*s.events, "unlock")
		if unlock != nil {
			unlock()
		}
	}, err
}
func (s *attemptCacheFake) SaveBatch(r BatchReceipt) error {
	s.saves++
	*s.events = append(*s.events, "save:"+r.Attempts[len(r.Attempts)-1].State)
	if s.saves == s.failSave {
		return failure("receipt_unavailable", "controlled cache failure")
	}
	return s.Store.SaveBatch(r)
}

type attemptLedgerFake struct {
	events                            *[]string
	prepared                          PreparedAttempt
	response                          AttemptResponse
	claim                             DispatchClaim
	prepareErr, claimErr, responseErr error
	won                               bool
	claimHook                         func()
}

func (l *attemptLedgerFake) Prepare(_ context.Context, r BatchReceipt, a PreparedAttempt) error {
	*l.events = append(*l.events, "prepare")
	l.prepared = a
	return l.prepareErr
}
func (l *attemptLedgerFake) Claim(_ context.Context, _ config.LaunchLedger, c DispatchClaim) (bool, error) {
	*l.events = append(*l.events, "claim")
	l.claim = c
	if l.claimHook != nil {
		l.claimHook()
	}
	return l.won, l.claimErr
}
func (l *attemptLedgerFake) RecordResponse(_ context.Context, _ config.LaunchLedger, r AttemptResponse) error {
	*l.events = append(*l.events, "response")
	l.response = r
	return l.responseErr
}

type attemptAPIFake struct {
	events *[]string
	calls  int
	input  *ec2.CreateFleetInput
}

func (a *attemptAPIFake) CreateFleet(_ context.Context, in *ec2.CreateFleetInput, _ ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error) {
	*a.events = append(*a.events, "fleet")
	a.calls++
	a.input = in
	return &ec2.CreateFleetOutput{FleetId: aws.String(fleetWireID), Errors: []types.CreateFleetError{},
		Instances: []types.CreateFleetInstance{{InstanceIds: []string{"i-0123456789abcdef0", "i-0123456789abcdef1"}, InstanceType: types.InstanceTypeC7i2xlarge,
			Lifecycle: types.InstanceLifecycleSpot, SubnetId: aws.String("subnet-12345678"), AvailabilityZone: aws.String("us-east-2a")}}}, nil
}

func attemptFixture(t *testing.T) (*AttemptService, config.Manifest, config.Profile, BatchReceipt, *attemptCacheFake, *attemptLedgerFake, *attemptAPIFake, *[]string) {
	t.Helper()
	c, m, p, selection := batchFixture(t)
	r, err := NewBatchReceipt(c, m, p, selection)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	cache := &attemptCacheFake{Store: Store{Dir: t.TempDir()}, events: &events}
	ledger := &attemptLedgerFake{events: &events, won: true}
	api := &attemptAPIFake{events: &events}
	s := &AttemptService{API: api, Scope: c, Cache: cache, Ledger: ledger,
		VerifyFoundation: func(context.Context, config.Manifest, config.Profile) error {
			events = append(events, "foundation")
			return nil
		},
		VerifyWorkers: func(_ context.Context, _ LaunchPlan, _ AttemptReceipt, workers []WorkerOutcome) ([]WorkerOutcome, error) {
			events = append(events, "observe")
			for n := range workers {
				workers[n].Status = "allocated"
				workers[n].Volumes = []Volume{{ID: "vol-0123456789abcdef" + string(rune('0'+n)), Device: "/dev/sda1", Root: true, DeleteOnTermination: true}}
			}
			return workers, nil
		},
	}
	return s, m, p, r, cache, ledger, api, &events
}

func TestAttemptDurableOrderingAndImmutableAnnouncement(t *testing.T) {
	s, m, p, r, cache, ledger, api, events := attemptFixture(t)
	hash := r.PlanSHA256
	result, err := s.Dispatch(context.Background(), m, p, r, func(copy BatchReceipt, path string) error {
		*events = append(*events, "announce")
		if path != cache.Path(r.RequestID) {
			t.Fatal("wrong recovery path")
		}
		saved, err := cache.LoadBatch(r.RequestID)
		if err != nil || saved.Attempts[0].State != "prepared" {
			t.Fatal("announcement preceded durable preparation")
		}
		copy.Plan.CreationTags["Owner"] = "changed"
		copy.Plan.Choices[0].SubnetID = "subnet-aaaaaaaa"
		copy.Attempts[0].ClientToken = "changed"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lock", "foundation", "save:prepared", "announce", "prepare", "save:dispatched", "claim", "fleet", "save:complete", "response", "observe", "unlock"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("ordering %v", *events)
	}
	if api.calls != 1 || !result.Dispatched || len(result.Workers) != 2 || result.Receipt.PlanSHA256 != hash || r.Plan.Digest() != hash {
		t.Fatalf("bad dispatch %+v", result)
	}
	if out := result.Outcome(); out.RequestedCount != 2 || out.FulfilledCount != 2 || out.MissingCount == nil || *out.MissingCount != 0 {
		t.Fatalf("incorrect verified counts: %+v", out)
	}
	if len(ledger.response.Workers[0].Volumes) != 0 || ledger.response.Workers[0].Status != "not_observed" {
		t.Fatal("worker observation rewrote the original shared response")
	}
	if ledger.claim.InputSHA256 != fleetInputDigest(api.input) || ledger.prepared.InputSHA256 != ledger.claim.InputSHA256 || ledger.response.InputSHA256 != ledger.claim.InputSHA256 ||
		ledger.claim.ClientToken != aws.ToString(api.input.ClientToken) || ledger.response.Attempt.State != "complete" {
		t.Fatal("shared records do not pin the dispatched input and response")
	}
	saved, err := cache.LoadBatch(r.RequestID)
	if err != nil || !reflect.DeepEqual(saved.Attempts[0].InstanceIDs, result.Receipt.Attempts[0].InstanceIDs) {
		t.Fatal("known IDs not saved")
	}
}

func TestAttemptStopsBeforeDispatchOnEveryPreparationFailure(t *testing.T) {
	for _, name := range []string{"invalid", "pins", "foundation", "lock", "save-prepared", "announce", "shared-prepare", "save-dispatched", "claim-exists", "claim-ambiguous", "cancel-after-claim"} {
		t.Run(name, func(t *testing.T) {
			s, m, p, r, cache, ledger, api, _ := attemptFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			announce := func(BatchReceipt, string) error { return nil }
			switch name {
			case "invalid":
				r.Attempts[0].ClientToken = "bad"
			case "pins":
				p.DiskGB++
			case "foundation":
				s.VerifyFoundation = func(context.Context, config.Manifest, config.Profile) error { return errors.New("drift") }
			case "lock":
				cache.lockErr = errors.New("lock failed")
			case "save-prepared":
				cache.failSave = 1
			case "announce":
				announce = func(BatchReceipt, string) error { return errors.New("closed output") }
			case "shared-prepare":
				ledger.prepareErr = errors.New("shared pins differ")
			case "save-dispatched":
				cache.failSave = 2
			case "claim-exists":
				ledger.won = false
			case "claim-ambiguous":
				ledger.claimErr = errors.New("write may have committed")
			case "cancel-after-claim":
				ledger.claimHook = cancel
			}
			result, err := s.Dispatch(ctx, m, p, r, announce)
			if err == nil || api.calls != 0 || result.Dispatched {
				t.Fatalf("unsafe dispatch: %d, %v", api.calls, err)
			}
		})
	}
}

func TestAttemptPostDispatchPersistenceFailureRetainsAllIdentities(t *testing.T) {
	for _, name := range []string{"local", "shared", "both", "observation"} {
		t.Run(name, func(t *testing.T) {
			s, m, p, r, cache, ledger, api, events := attemptFixture(t)
			if name == "local" || name == "both" {
				cache.failSave = 3
			}
			if name == "shared" || name == "both" {
				ledger.responseErr = errors.New("response write failed")
			}
			if name == "observation" {
				verify := s.VerifyWorkers
				s.VerifyWorkers = func(ctx context.Context, p LaunchPlan, a AttemptReceipt, w []WorkerOutcome) ([]WorkerOutcome, error) {
					w, _ = verify(ctx, p, a, w)
					return w, errors.New("observation failed")
				}
			}
			result, err := s.Dispatch(context.Background(), m, p, r, func(BatchReceipt, string) error { return nil })
			if err == nil || api.calls != 1 || len(result.Receipt.Attempts[0].InstanceIDs) != 2 || result.Receipt.Attempts[0].FleetID != fleetWireID || len(result.Workers) != 2 {
				t.Fatalf("lost accepted IDs: %+v, %v", result, err)
			}
			for _, w := range result.Workers {
				if len(w.Volumes) != 1 {
					t.Fatal("lost known root mapping")
				}
			}
			if name == "observation" && result.Outcome().MissingCount != nil {
				t.Fatal("failed observation authorized missing capacity")
			}
			if !strings.Contains(strings.Join(*events, ","), "response,observe") {
				t.Fatal("one persistence failure skipped independent evidence")
			}
		})
	}
}

func TestAttemptSDKRejectionAfterUncertainRetryRemainsUnknown(t *testing.T) {
	for _, retryFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "single-denial", true: "uncertain-then-denial"}[retryFirst], func(t *testing.T) {
			s, m, p, r, _, ledger, _, _ := attemptFixture(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = io.Copy(io.Discard, r.Body)
				if retryFirst && calls == 1 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>SECRET credential detail</Message></Error></Errors><RequestID>test</RequestID></Response>`)
			}))
			defer server.Close()
			s.API = fleetWireClient(server, 2)
			result, err := s.Dispatch(context.Background(), m, p, r, func(BatchReceipt, string) error { return nil })
			wantState, wantCalls := "rejected", 1
			if retryFirst {
				wantState, wantCalls = "unknown", 2
			}
			if result.Receipt.Attempts[0].State != wantState || ledger.response.Attempt.State != wantState || calls != wantCalls {
				t.Fatalf("retry evidence lost: state=%s calls=%d err=%v", result.Receipt.Attempts[0].State, calls, err)
			}
			if retryFirst && err == nil {
				t.Fatal("uncertain allocation reported successful")
			}
			for _, e := range result.Receipt.Attempts[0].Errors {
				if strings.Contains(e.Message, "SECRET") {
					t.Fatal("raw provider message leaked")
				}
			}
		})
	}
}

func TestAttemptCountsRequireExactVerifiedAttemptIdentities(t *testing.T) {
	for _, name := range []string{"wrong-id", "wrong-attempt", "unobserved", "matching"} {
		t.Run(name, func(t *testing.T) {
			r := receiptFixture(t)
			a := r.Attempts[0]
			w := WorkerOutcome{Instance: Instance{ID: a.InstanceIDs[0], AttemptID: a.AttemptID}, Status: "allocated"}
			switch name {
			case "wrong-id":
				w.ID = "i-23456789"
			case "wrong-attempt":
				w.AttemptID = strings.Repeat("f", 32)
			case "unobserved":
				w.Status = "not_observed"
			}
			out := (AttemptResult{Receipt: r, Workers: []WorkerOutcome{w}}).Outcome()
			if name == "matching" {
				if out.FulfilledCount != 1 || out.MissingCount == nil || *out.MissingCount != 1 {
					t.Fatalf("verified partial counts lost: %+v", out)
				}
			} else if out.FulfilledCount != 0 || out.MissingCount != nil {
				t.Fatalf("foreign or unverified identity counted: %+v", out)
			}
		})
	}
}
