package expiry

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }
func instant(t *testing.T, value string) time.Time {
	t.Helper()
	result, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func wantReason(t *testing.T, err error, code Reason) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("got %v; want %s", err, code)
	}
}
func ptr(s string) *string { return &s }

func TestTTLGrammarAndPrecedence(t *testing.T) {
	for value, want := range map[string]time.Duration{"2h": DefaultTTL, "36h": 36 * time.Hour, "168h": MaxTTL, "1h30m": 90 * time.Minute, ".5s": time.Second / 2, "1ns": time.Nanosecond, "1us": time.Microsecond, "1µs": time.Microsecond, "1μs": time.Microsecond} {
		got, err := ParseTTL(value)
		if err != nil || got != want {
			t.Fatalf("%s = %s, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "0", "0s", "-1h", "+1h", "1h-1m", " 2h", "2h\n", "7d", "1e3s", "NaN", "Inf", "unlimited", "169h", "168h1ns", "9223372036854775808ns", "999999999999999999999999999h", "0.1ns"} {
		_, err := ParseTTL(value)
		wantReason(t, err, TTLInvalid)
	}
	for _, test := range []struct {
		config, flag *string
		want         time.Duration
	}{{nil, nil, DefaultTTL}, {ptr("36h"), nil, 36 * time.Hour}, {ptr("36h"), ptr("1h"), time.Hour}} {
		got, err := ResolveTTL(test.config, test.flag)
		if err != nil || got != test.want {
			t.Fatalf("resolution: %s %v", got, err)
		}
	}
	for _, pair := range [][2]*string{{ptr(""), nil}, {nil, ptr("")}, {ptr("bad"), ptr("1h")}} {
		_, err := ResolveTTL(pair[0], pair[1])
		wantReason(t, err, TTLInvalid)
	}
}

func TestUTCPrecisionAndExpiryBoundary(t *testing.T) {
	now := instant(t, "2026-09-14T01:00:00.123456789-05:00")
	w, err := NewWindow(fixedClock{now}, 36*time.Hour)
	if err != nil || w.CreatedAt != "2026-09-14T06:00:00.123456789Z" || w.ExpiresAt != "2026-09-15T18:00:00.123456789Z" {
		t.Fatalf("%+v %v", w, err)
	}
	deadline := instant(t, w.ExpiresAt)
	if err := CheckAllocation(LaunchPlanSchema, w, deadline.Add(-time.Nanosecond), false); err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{deadline, deadline.Add(time.Nanosecond), deadline.In(time.FixedZone("local", -5*3600))} {
		wantReason(t, CheckAllocation(LaunchPlanSchema, w, now, false), RequestExpired)
	}
	for _, value := range []string{"2026-09-14T06:00:00+00:00", "2026-09-14T06:00:00.000Z", "2026-09-14T06:00:00.1234567890Z", "2026-09-14t06:00:00z", "2026-09-14T06:00:60Z", ""} {
		_, err := ParseTimestamp(value)
		wantReason(t, err, ExpiryInvalid)
	}
	_, err = NewWindow(fixedClock{instant(t, "9999-12-31T23:59:59Z")}, time.Hour)
	wantReason(t, err, ExpiryInvalid)
	_, err = NewWindow(fixedClock{}, DefaultTTL)
	wantReason(t, err, ClockInvalid)
	_, err = NewWindow(nil, DefaultTTL)
	wantReason(t, err, ClockInvalid)
}

func TestImmutableRecoveryAndExpiredMissingCapacity(t *testing.T) {
	w, err := NewWindow(fixedClock{instant(t, "2026-09-14T00:00:00Z")}, DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	original := w
	// Known fulfillment does not change the remaining workers' deadline; no
	// successor/restart may turn an expired missing-capacity request into a send.
	for _, kind := range []string{"initial delayed dispatch", "resume prepared", "retry missing", "SDK retry"} {
		t.Run(kind, func(t *testing.T) {
			wantReason(t, CheckAllocation(2, w, instant(t, w.ExpiresAt), false), RequestExpired)
		})
	}
	wantReason(t, CheckAllocation(2, w, instant(t, w.CreatedAt), true), ReplayOverride)
	wantReason(t, CheckAllocation(1, Window{CreatedAt: w.CreatedAt}, instant(t, w.CreatedAt), false), LegacyRequest)
	if !reflect.DeepEqual(w, original) {
		t.Fatal("allocation guard mutated immutable deadline")
	}
	// Validation is independent of current time, so expired records remain readable.
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Window{{w.CreatedAt, w.CreatedAt}, {w.ExpiresAt, w.CreatedAt}, {w.CreatedAt, "2026-09-22T00:00:00Z"}} {
		wantReason(t, bad.Validate(), ExpiryInvalid)
	}
}

func TestVersionedPlanFieldsAndImmutableTagAgreement(t *testing.T) {
	w := Window{"2026-09-14T00:00:00Z", "2026-09-14T02:00:00Z"}
	tags := map[string]string{"CreatedAt": w.CreatedAt, TagKey: w.ExpiresAt}
	if err := ValidatePlanFields(LaunchPlanSchema, w, tags); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		schema int
		window Window
		tags   map[string]string
	}{
		{1, w, tags}, {3, w, tags}, {2, w, nil}, {2, w, map[string]string{"CreatedAt": w.CreatedAt, TagKey: "2026-09-14T03:00:00Z"}},
		{2, w, map[string]string{"CreatedAt": "2026-09-13T00:00:00Z", TagKey: w.ExpiresAt}},
	} {
		wantReason(t, ValidatePlanFields(test.schema, test.window, test.tags), ExpiryInvalid)
	}
	if err := ValidatePlanFields(1, Window{CreatedAt: "2026-09-14T00:00:00+00:00"}, map[string]string{"CreatedAt": "2026-09-14T00:00:00+00:00"}); err != nil {
		t.Fatal("legacy spelling was reinterpreted", err)
	}
}
