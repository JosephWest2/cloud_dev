package expiry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func cleanupFixture() (Scope, Resource) {
	scope := Scope{"123456789012", "us-east-2", "personal-dev", "owner"}
	resource := Resource{ID: "i-0123456789abcdef0", Account: scope.Account, Region: scope.Region, State: "running", Tags: []Tag{{ptr("ManagedBy"), ptr("devbox")}, {ptr("Deployment"), ptr(scope.Deployment)}, {ptr("Owner"), ptr(scope.Owner)}, {ptr(TagKey), ptr("2026-09-14T02:00:00Z")}}}
	return scope, resource
}

func TestEligibilityScopeTagsStatesAndUTC(t *testing.T) {
	now := instant(t, "2026-09-14T02:00:00Z")
	cases := []struct {
		name     string
		mutate   func(*Resource)
		reason   Reason
		eligible bool
	}{
		{"boundary", func(*Resource) {}, Expired, true},
		{"account", func(r *Resource) { r.Account = "999999999999" }, ScopeMismatch, false},
		{"region", func(r *Resource) { r.Region = "us-west-2" }, ScopeMismatch, false},
		{"identity", func(r *Resource) { r.ID = "garbage" }, ResourceInvalid, false},
		{"owner", func(r *Resource) { r.Tags[2].Value = ptr("someone-else") }, ScopeMismatch, false},
		{"deployment", func(r *Resource) { r.Tags[1].Value = ptr("another") }, ScopeMismatch, false},
		{"unmanaged", func(r *Resource) { r.Tags[0].Value = ptr("other") }, ScopeMismatch, false},
		{"missing", func(r *Resource) { r.Tags = r.Tags[:3] }, ExpiryMissing, false},
		{"malformed", func(r *Resource) { r.Tags[3].Value = ptr("tomorrow") }, ExpiryInvalid, false},
		{"null-expiry", func(r *Resource) { r.Tags[3].Value = nil }, ExpiryInvalid, false},
		{"duplicate-expiry", func(r *Resource) { r.Tags = append(r.Tags, r.Tags[3]) }, ExpiryDuplicate, false},
		{"duplicate-owner", func(r *Resource) { r.Tags = append(r.Tags, r.Tags[2]) }, TagsDuplicate, false},
		{"null-key", func(r *Resource) { r.Tags = append(r.Tags, Tag{}) }, TagsInvalid, false},
		{"future", func(r *Resource) { r.Tags[3].Value = ptr("2026-09-14T02:00:00.000000001Z") }, ExpiryFuture, false},
		{"pending", func(r *Resource) { r.State = "pending" }, Expired, true},
		{"stopping", func(r *Resource) { r.State = "stopping" }, Expired, true},
		{"stopped", func(r *Resource) { r.State = "stopped" }, Expired, true},
		{"shutting-down", func(r *Resource) { r.State = "shutting-down" }, AlreadyTerminating, false},
		{"terminated", func(r *Resource) { r.State = "terminated" }, AlreadyTerminated, false},
		{"unknown", func(r *Resource) { r.State = "unknown" }, StateUnknown, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			scope, r := cleanupFixture()
			test.mutate(&r)
			d := Evaluate(scope, r, now)
			if d.Reason != test.reason || d.Eligible != test.eligible {
				t.Fatalf("%+v", d)
			}
		})
	}
	scope, r := cleanupFixture()
	if d := Evaluate(scope, r, now.Add(-time.Nanosecond)); d.Reason != ExpiryFuture {
		t.Fatal(d)
	}
	if d := Evaluate(scope, r, now.In(time.FixedZone("local", 3600))); !d.Eligible {
		t.Fatal(d)
	}
	if d := Evaluate(scope, r, time.Time{}); d.Reason != ClockInvalid {
		t.Fatal(d)
	}
	scope.Owner = ""
	if d := Evaluate(scope, r, now); d.Reason != ScopeInvalid {
		t.Fatal(d)
	}
}

func TestRecheckCannotExpandSelectionOrAcceptChangedExpiry(t *testing.T) {
	scope, r := cleanupFixture()
	now := instant(t, "2026-09-14T02:00:00Z")
	selected := Evaluate(scope, r, now)
	for _, test := range []struct {
		name   string
		mutate func(*Resource)
		reason Reason
	}{
		{"same", func(*Resource) {}, Expired},
		{"different-ID", func(r *Resource) { r.ID = "i-11111111111111111" }, ResourceChanged},
		{"changed-past", func(r *Resource) { r.Tags[3].Value = ptr("2026-09-14T01:00:00Z") }, ExpiryChanged},
		{"changed-future", func(r *Resource) { r.Tags[3].Value = ptr("2026-09-14T03:00:00Z") }, ExpiryChanged},
		{"removed", func(r *Resource) { r.Tags = r.Tags[:3] }, ExpiryMissing},
		{"scope-drift", func(r *Resource) { r.Tags[2].Value = ptr("other") }, ScopeMismatch},
		{"concurrent", func(r *Resource) { r.State = "shutting-down" }, AlreadyTerminating},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, fresh := cleanupFixture()
			test.mutate(&fresh)
			d := Recheck(scope, selected, fresh, now)
			if d.Reason != test.reason || d.Eligible != (test.reason == Expired) {
				t.Fatal(d)
			}
		})
	}
	d := Recheck(scope, Decision{}, r, now)
	if d.Eligible || d.Reason != ResourceChanged {
		t.Fatal(d)
	}
}

func TestInvalidExpiryJSONDoesNotEchoRawTag(t *testing.T) {
	scope, r := cleanupFixture()
	r.Tags[3].Value = ptr("sensitive arbitrary tag")
	d := Evaluate(scope, r, instant(t, "2026-09-14T02:00:00Z"))
	b, err := json.Marshal(d)
	if err != nil || !strings.Contains(string(b), `"expires_at":null`) || strings.Contains(string(b), "sensitive") {
		t.Fatalf("%s %v", b, err)
	}
}
