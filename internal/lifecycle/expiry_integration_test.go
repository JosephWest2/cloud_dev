package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
)

type expiryTestClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *expiryTestClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *expiryTestClock) set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.value = t }
func expiryTime(t *testing.T, value string) time.Time {
	t.Helper()
	v, e := expiry.ParseTimestamp(value)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) || f.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func repinExpiry(r *BatchReceipt, legacy bool) {
	if legacy {
		r.SchemaVersion = 2
		r.Plan.SchemaVersion = 1
		r.Plan.ExpiresAt = ""
		delete(r.Plan.CreationTags, "ExpiresAt")
	}
	r.PlanSHA256 = r.Plan.Digest()
	for n := range r.Attempts {
		a := &r.Attempts[n]
		a.ClientToken = attemptToken(r.PlanSHA256, a.AttemptID, a.RequestedCount)
	}
}

func TestExpiryCreationWindowAndConfigurationPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, configured, override, want string
		configSet, overrideSet           bool
	}{
		{name: "default", want: "2h0m0s"},
		{name: "config", configured: "36h", configSet: true, want: "36h0m0s"},
		{name: "override", configured: "3h", override: ".5h", configSet: true, overrideSet: true, want: "30m0s"},
		{name: "maximum", override: "168h", overrideSet: true, want: "168h0m0s"},
		{name: "nanosecond", override: "1ns", overrideSet: true, want: "1ns"},
		{name: "empty-config", configSet: true},
		{name: "empty-override", overrideSet: true},
		{name: "invalid-config-overridden", configured: "unlimited", override: "2h", configSet: true, overrideSet: true},
		{name: "too-long", override: "168h1ns", overrideSet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m, p, s := batchFixture(t)
			if tc.configSet {
				c.DefaultTTL = &tc.configured
			}
			if tc.overrideSet {
				s.TTL = &tc.override
			}
			clock := fixedClock{time.Date(2026, 9, 14, 3, 4, 5, 123456789, time.FixedZone("creation", -5*3600))}
			r, err := NewBatchReceiptWithClock(c, m, p, s, clock)
			if tc.want == "" {
				requireCode(t, err, "ttl_invalid")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			duration, _ := r.Plan.Window().TTL()
			if r.SchemaVersion != 3 || r.Plan.SchemaVersion != 2 || duration.String() != tc.want || r.Plan.CreatedAt != "2026-09-14T08:04:05.123456789Z" || r.Plan.CreationTags["ExpiresAt"] != r.Plan.ExpiresAt || r.Attempts[0].CreatedAt != r.Plan.CreatedAt {
				t.Fatalf("wrong immutable window: %+v", r)
			}
			raw, _ := json.Marshal(r)
			decoded, err := decodeBatchReceipt(raw)
			if err != nil || !reflect.DeepEqual(decoded, r) {
				t.Fatal("expiry receipt round trip", err)
			}
		})
	}
	c, m, p, s := batchFixture(t)
	for _, now := range []time.Time{{}, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)} {
		if _, err := NewBatchReceiptWithClock(c, m, p, s, fixedClock{now}); err == nil {
			t.Fatal("invalid/overflow clock accepted")
		}
	}
}

func TestExpiryDecodersRejectPresenceUnknownFieldsAndVersionMismatch(t *testing.T) {
	old, err := os.ReadFile("testdata/expiry-legacy/plan-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"expires_at":""`, `"expires_at":null`, `"expires_at":"2026-09-14T02:00:00Z"`, `"unknown":true`} {
		data := bytes.Replace(old, []byte(`"schema_version":1`), []byte(`"schema_version":1,`+field), 1)
		var plan LaunchPlan
		if json.Unmarshal(data, &plan) == nil {
			t.Fatalf("accepted %s", field)
		}
	}
	c, m, p, s := batchFixture(t)
	r, err := NewBatchReceipt(c, m, p, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"pair", "expiry-tag", "created-tag", "noncanonical", "unknown-version"} {
		t.Run(variant, func(t *testing.T) {
			candidate := cloneBatch(r)
			switch variant {
			case "pair":
				candidate.SchemaVersion = 2
			case "expiry-tag":
				candidate.Plan.CreationTags["ExpiresAt"] = ""
			case "created-tag":
				candidate.Plan.CreationTags["CreatedAt"] = "wrong"
			case "noncanonical":
				candidate.Plan.ExpiresAt = "2026-09-14T02:00:00+00:00"
			case "unknown-version":
				candidate.Plan.SchemaVersion = 3
			}
			candidate.PlanSHA256 = candidate.Plan.Digest()
			for n := range candidate.Attempts {
				a := &candidate.Attempts[n]
				a.ClientToken = attemptToken(candidate.PlanSHA256, a.AttemptID, a.RequestedCount)
			}
			data, _ := json.Marshal(candidate)
			if _, err := decodeBatchReceipt(data); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			if _, err := BuildFleetInput(candidate.Plan, candidate.Attempts[0]); variant != "pair" && err == nil {
				t.Fatal("invalid pure serializer input accepted")
			}
		})
	}
}

func TestExpiryGateAtPreparationClaimAndSend(t *testing.T) {
	for _, stage := range []string{"initial", "legacy", "after-preview", "after-claim", "boundary-minus-1ns", "old-manifest"} {
		t.Run(stage, func(t *testing.T) {
			s, m, p, r, _, ledger, api, events := attemptFixture(t)
			deadline := expiryTime(t, r.Plan.ExpiresAt)
			clock := &expiryTestClock{value: deadline.Add(-time.Second)}
			s.Clock = clock
			announce := func(BatchReceipt, string) error { return nil }
			switch stage {
			case "initial":
				clock.set(deadline)
			case "legacy":
				repinExpiry(&r, true)
			case "after-preview":
				announce = func(BatchReceipt, string) error { clock.set(deadline); return nil }
			case "after-claim":
				ledger.claimHook = func() { clock.set(deadline) }
			case "boundary-minus-1ns":
				clock.set(deadline.Add(-time.Nanosecond))
			case "old-manifest":
				m.SchemaVersion = 5
			}
			out, err := s.Dispatch(context.Background(), m, p, r, announce)
			if stage == "boundary-minus-1ns" {
				if err != nil || api.calls != 1 {
					t.Fatal(err, api.calls)
				}
				return
			}
			want := "request_expired"
			if stage == "legacy" {
				want = "legacy_request_no_expiry"
			}
			if stage == "old-manifest" {
				want = "manifest_upgrade_required"
			}
			requireCode(t, err, want)
			if api.calls != 0 || out.Dispatched {
				t.Fatal("expired/legacy send", *events)
			}
			if stage == "after-claim" {
				if ledger.claim.AttemptID == "" || out.Receipt.Attempts[0].State != "dispatched" {
					t.Fatal("lost permanent claim evidence")
				}
				ledger.won = false
				clock.set(deadline.Add(-time.Second))
				_, err = s.Dispatch(context.Background(), m, p, r, func(BatchReceipt, string) error { return nil })
				if err == nil || api.calls != 0 {
					t.Fatal("claimed unsent request reused")
				}
			} else if ledger.prepared.RequestID != "" || ledger.claim.AttemptID != "" {
				t.Fatal("gate ran after shared mutation", *events)
			}
		})
	}
}

func TestExpirySDKFinalSendGateAfterSerialization(t *testing.T) {
	s, m, p, r, _, ledger, _, _ := attemptFixture(t)
	deadline := expiryTime(t, r.Plan.ExpiresAt)
	clock := &expiryTestClock{value: deadline.Add(-time.Second)}
	s.Clock = clock
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, fleetWireSuccess("spot"))
	}))
	defer server.Close()
	client := fleetWireClient(server, 5)
	clientOptions := client.Options()
	clientOptions.APIOptions = append(clientOptions.APIOptions, func(stack *middleware.Stack) error {
		return stack.Build.Add(middleware.BuildMiddlewareFunc("DelayAfterSerialization", func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
			clock.set(deadline)
			return next.HandleBuild(ctx, in)
		}), middleware.After)
	})
	s.API = ec2.New(clientOptions)
	out, err := s.Dispatch(context.Background(), m, p, r, func(BatchReceipt, string) error { return nil })
	requireCode(t, err, "request_expired")
	if calls != 0 || ledger.claim.AttemptID == "" || out.Receipt.Attempts[0].State != "unknown" {
		t.Fatalf("unsafe delayed send: calls=%d out=%+v", calls, out)
	}
}

func TestExpiryRecoveryRetainsOriginalWindowAndHistoricFulfillment(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			l, storage, c, r, _ := ledgerFixture(t)
			deadline := expiryTime(t, r.Plan.ExpiresAt)
			repinExpiry(&r, legacy)
			r = ledgerPublishResponse(t, l, r, ledgerPrepared(t, r), "i-12345678")
			fleet := &recoveryFleetAPI{}
			s := newRecoveryFixtureService(t, l, c, fleet)
			clock := &expiryTestClock{value: deadline}
			s.Clock = clock
			s.Dispatcher.Clock = clock
			_, m, p, _ := batchFixture(t)
			before := map[string]string{}
			for k, v := range storage.objects {
				before[k] = string(v)
			}
			out, err := s.Resume(context.Background(), r.RequestID)
			if err != nil || out.FulfilledCount != 1 || len(out.Workers) != 1 || out.MissingCount == nil || *out.MissingCount != 1 {
				t.Fatalf("observation blocked: %+v %v", out, err)
			}
			out, err = s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(BatchReceipt, string) error { t.Fatal("expired retry announced"); return nil })
			want := "request_expired"
			if legacy {
				want = "legacy_request_no_expiry"
			}
			requireCode(t, err, want)
			if len(fleet.counts) != 0 || out.FulfilledCount != 1 || len(storage.objects) != len(before) {
				t.Fatal("expired historic fulfillment replaced")
			}
			for k, v := range before {
				if string(storage.objects[k]) != v {
					t.Fatal("permanent history rewritten", k)
				}
			}
			if legacy {
				return
			}
			// A restart before the same deadline can allocate only the original remainder;
			// changed/invalid config TTL is ignored and never recomputed.
			clock.set(deadline.Add(-time.Minute))
			bad := "unlimited"
			s.Scope.DefaultTTL = &bad
			s.Dispatcher.Scope.DefaultTTL = &bad
			out, err = s.RetryMissing(context.Background(), m, p, r.RequestID, r.Attempts[0].AttemptID, func(next BatchReceipt, _ string) error {
				if next.Plan.ExpiresAt != r.Plan.ExpiresAt || next.Plan.CreatedAt != r.Plan.CreatedAt || next.PlanSHA256 != r.PlanSHA256 {
					t.Fatal("retry extended original lifetime")
				}
				return nil
			})
			if err != nil || !reflect.DeepEqual(fleet.counts, []int{1}) || out.FulfilledCount != 2 {
				t.Fatalf("retry: %+v %v", out, err)
			}
			clock.set(deadline.Add(time.Hour))
			out, err = s.RetryMissing(context.Background(), m, p, r.RequestID, out.Attempts[1].AttemptID, func(BatchReceipt, string) error { t.Fatal("full historical request replaced"); return nil })
			if err != nil || out.FulfilledCount != 2 || len(fleet.counts) != 1 {
				t.Fatalf("historic full request: %+v %v", out, err)
			}
		})
	}
}

func TestExpiryInventoryDiagnosticsDoNotPreventExplicitDown(t *testing.T) {
	deadline := "2026-09-14T02:00:00.000000001Z"
	for _, tc := range []struct {
		name, status string
		values       []*string
	}{
		{name: "absent", status: "missing"}, {name: "malformed", status: "invalid", values: []*string{aws.String("SECRET invalid")}},
		{name: "nil", status: "invalid", values: []*string{nil}}, {name: "duplicate", status: "duplicate", values: []*string{&deadline, &deadline}},
		{name: "future", status: "future", values: []*string{&deadline}}, {name: "equality", status: "expired", values: []*string{&deadline}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeEC2{}
			api.describe = func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				for _, f := range in.Filters {
					if strings.Contains(aws.ToString(f.Name), "ExpiresAt") {
						t.Fatal("expiry filtered inventory")
					}
				}
				i := worker("i-12345678")
				for _, value := range tc.values {
					i.Tags = append(i.Tags, types.Tag{Key: aws.String("ExpiresAt"), Value: value})
				}
				if api.terminations > 0 {
					i.State.Name = types.InstanceStateNameTerminated
				}
				return inventory(i), nil
			}
			api.volumes = func(*ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
				return nil, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound"}
			}
			s := testService(api)
			now := expiryTime(t, deadline)
			if tc.name == "future" {
				now = now.Add(-time.Nanosecond)
			}
			s.Clock = fixedClock{now}
			listed, err := s.List(context.Background())
			if err != nil || len(listed) != 1 || listed[0].ExpiryStatus != tc.status {
				t.Fatalf("inventory: %+v %v", listed, err)
			}
			raw, _ := json.Marshal(PublicResult(Result{Outcome: Outcome{Instances: listed}}))
			if !bytes.Contains(raw, []byte(`"expiry_status":"`+tc.status+`"`)) || bytes.Contains(raw, []byte("SECRET")) {
				t.Fatal(string(raw))
			}
			if tc.status != "future" && tc.status != "expired" && !bytes.Contains(raw, []byte(`"expires_at":null`)) {
				t.Fatal(string(raw))
			}
			out, err := s.Down(context.Background(), "i-12345678")
			if err != nil || api.terminations != 1 || len(out.Instances) != 1 || out.Instances[0].RootDeletion != "deleted" {
				t.Fatalf("expiry blocked deliberate down: %+v %v", out, err)
			}
		})
	}
	for _, tag := range []types.Tag{{Key: aws.String("Owner"), Value: aws.String("test-owner")}, {Key: aws.String("Owner"), Value: nil}, {Value: aws.String("bad")}} {
		i := worker("i-12345678")
		i.Tags = append(i.Tags, tag, types.Tag{Key: aws.String("ExpiresAt"), Value: nil})
		api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(i), nil }}
		_, err := testService(api).Down(context.Background(), "i-12345678")
		if err == nil || api.terminations != 0 {
			t.Fatal("scope validation weakened")
		}
	}
}

func TestExpiryFleetSingleBatchMarketsActualWireTags(t *testing.T) {
	for _, market := range []string{"spot", "on-demand"} {
		for _, count := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s-%d", market, count), func(t *testing.T) {
				s, m, p, _, _, ledger, _, _ := attemptFixture(t)
				c := s.Scope
				selection := LaunchSelection{Profile: "agent", Name: "worker", Count: count, OnDemand: market == "on-demand"}
				ttl := "1h30m"
				selection.TTL = &ttl
				now := expiryTime(t, "2026-09-14T00:00:00.123456789Z")
				s.Clock = fixedClock{now}
				r, err := NewBatchReceiptWithClock(c, m, p, selection, s.Clock)
				if err != nil {
					t.Fatal(err)
				}
				var got url.Values
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if err := req.ParseForm(); err != nil {
						t.Error(err)
					}
					got = req.PostForm
					if ledger.claim.AttemptID == "" || ledger.prepared.PlanSHA256 != r.Plan.Digest() {
						t.Error("send before immutable persistence")
					}
					xml := fleetWireSuccess(market)
					if count == 1 {
						xml = strings.Replace(xml, "<item>i-0123456789abcdef1</item>", "", 1)
					}
					_, _ = io.WriteString(w, xml)
				}))
				defer server.Close()
				s.API = fleetWireClient(server, 3)
				out, err := s.Dispatch(context.Background(), m, p, r, func(receipt BatchReceipt, _ string) error {
					if !strings.Contains(receipt.Plan.Preview(), "ttl=1h30m0s expires_at=2026-09-14T01:30:00.123456789Z") {
						t.Error("missing preview expiry")
					}
					return nil
				})
				if err != nil || len(out.Workers) != count {
					t.Fatalf("%+v %v", out, err)
				}
				if got.Get("Action") != "CreateFleet" || got.Get("Type") != "instant" || got.Get("TargetCapacitySpecification.TotalTargetCapacity") != fmt.Sprint(count) {
					t.Fatal("wrong allocation API", got)
				}
				for n, resource := range []string{"fleet", "instance", "volume"} {
					prefix := fmt.Sprintf("TagSpecification.%d.", n+1)
					if got.Get(prefix+"ResourceType") != resource {
						t.Fatal("resource absent", resource)
					}
					tags := map[string]string{}
					for j := 1; ; j++ {
						key := got.Get(prefix + fmt.Sprintf("Tag.%d.Key", j))
						if key == "" {
							break
						}
						tags[key] = got.Get(prefix + fmt.Sprintf("Tag.%d.Value", j))
					}
					if tags["CreatedAt"] != "2026-09-14T00:00:00.123456789Z" || tags["ExpiresAt"] != "2026-09-14T01:30:00.123456789Z" || tags["RequestId"] != r.RequestID {
						t.Fatal("wrong exact creation tags", tags)
					}
				}
			})
		}
	}
}

func TestExpiryV2FixturesPinReceiptAndPermanentRecords(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		b, e := os.ReadFile("testdata/expiry-v2/" + name)
		if e != nil {
			t.Fatal(e)
		}
		return bytes.TrimSuffix(b, []byte("\n"))
	}
	var plan LaunchPlan
	var batch BatchReceipt
	var prepared PreparedAttempt
	var claim DispatchClaim
	var response AttemptResponse
	for name, value := range map[string]any{"plan-v2": &plan, "batch-v3": &batch, "prepared-v1": &prepared, "claim-v1": &claim, "response-v1": &response} {
		b := read(name + ".json")
		if err := json.Unmarshal(b, value); err != nil {
			t.Fatal(name, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil || !bytes.Equal(encoded, b) {
			t.Fatal("expiry wire bytes changed", name, err)
		}
	}
	if plan.Digest() != string(read("plan-v2.sha256")) || batch.PlanSHA256 != plan.Digest() {
		t.Fatal("expiry digest mismatch")
	}
	decoded, err := decodeBatchReceipt(read("batch-v3.json"))
	if err != nil || decoded.SchemaVersion != 3 || decoded.Plan.SchemaVersion != 2 {
		t.Fatal("version pair", err)
	}
	batch.Attempts = nil
	if !validPrepared(batch, prepared) || claim != expectedClaim(prepared) || !validResponse(batch, prepared, response) {
		t.Fatal("expiry permanent records failed validation")
	}
	if response.Workers[0].ExpiresAt != plan.ExpiresAt || response.Workers[0].ExpiryStatus != "" {
		t.Fatal("permanent response contains time-varying status")
	}
}
