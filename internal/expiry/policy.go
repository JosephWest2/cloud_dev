// Package expiry defines the shared, cloud-independent expiry contract. It does
// not discover resources, authorize AWS calls, or extend an existing deadline.
package expiry

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultTTL         = 2 * time.Hour
	MaxTTL             = 168 * time.Hour
	ScheduleInterval   = 5 * time.Minute
	TagKey             = "ExpiresAt"
	LaunchPlanSchema   = 2
	BatchReceiptSchema = 3
	ManifestSchema     = 6
)

type Reason string

const (
	TTLInvalid         Reason = "ttl_invalid"
	ClockInvalid       Reason = "clock_invalid"
	ExpiryInvalid      Reason = "expiry_invalid"
	ExpiryMissing      Reason = "expiry_missing"
	ExpiryDuplicate    Reason = "expiry_duplicate"
	ExpiryFuture       Reason = "expiry_future"
	ExpiryChanged      Reason = "expiry_changed"
	Expired            Reason = "expired"
	RequestExpired     Reason = "request_expired"
	LegacyRequest      Reason = "legacy_request_no_expiry"
	ReplayOverride     Reason = "replay_override"
	ScopeInvalid       Reason = "scope_invalid"
	ScopeMismatch      Reason = "scope_mismatch"
	TagsInvalid        Reason = "tags_invalid"
	TagsDuplicate      Reason = "tags_duplicate"
	ResourceInvalid    Reason = "resource_invalid"
	ResourceChanged    Reason = "resource_changed"
	StateUnknown       Reason = "state_unknown"
	AlreadyTerminating Reason = "already_terminating"
	AlreadyTerminated  Reason = "already_terminated"
)

// Error exposes stable machine-readable reasons without echoing user input.
type Error struct{ Code Reason }

func (e *Error) Error() string { return string(e.Code) }
func reject(code Reason) error { return &Error{Code: code} }

// Clock is sampled at creation, discovery, and immediately before each dispatch.
// Adapters supply a nonnil clock; production uses SystemClock.
type Clock interface{ Now() time.Time }
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// ParseTTL accepts unsigned Go durations (ns, us, µs, μs, ms, s, m, h),
// including fractions/concatenation, with nanosecond resolution. There is no
// whitespace, day suffix, exponent, sign, zero, unlimited, or overflow coercion.
func ParseTTL(value string) (time.Duration, error) {
	if value == "" || strings.ContainsAny(value, "+- \t\r\n") {
		return 0, reject(TTLInvalid)
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 || d > MaxTTL {
		return 0, reject(TTLInvalid)
	}
	return d, nil
}

// ResolveTTL distinguishes omission (nil) from an explicitly empty value.
// Validate configured input even when overridden; callers invoke this only on
// fresh launch, so an invalid launch default cannot disable emergency cleanup.
func ResolveTTL(configured, override *string) (time.Duration, error) {
	d := DefaultTTL
	var err error
	if configured != nil {
		d, err = ParseTTL(*configured)
		if err != nil {
			return 0, err
		}
	}
	if override != nil {
		return ParseTTL(*override)
	}
	return d, nil
}

// Timestamp returns a canonical UTC RFC3339Nano value without a monotonic part.
func Timestamp(t time.Time) (string, error) {
	t = t.UTC().Round(0)
	if t.IsZero() || t.Year() < 1 || t.Year() > 9999 {
		return "", reject(ClockInvalid)
	}
	return t.Format(time.RFC3339Nano), nil
}

// ParseTimestamp rejects offsets (including +00:00), redundant fractional zeros,
// and other noncanonical spellings. Precision is up to nine fractional digits.
func ParseTimestamp(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, reject(ExpiryInvalid)
	}
	canonical, err := Timestamp(t)
	if err != nil || canonical != value {
		return time.Time{}, reject(ExpiryInvalid)
	}
	return t, nil
}

// Window is the immutable elapsed wall-time interval for an entire request.
// Embed these fields in plan v2; do not add them to historical plan JSON.
type Window struct {
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

func NewWindow(clock Clock, ttl time.Duration) (Window, error) {
	if clock == nil {
		return Window{}, reject(ClockInvalid)
	}
	if ttl <= 0 || ttl > MaxTTL {
		return Window{}, reject(TTLInvalid)
	}
	created, err := Timestamp(clock.Now())
	if err != nil {
		return Window{}, err
	}
	t, _ := ParseTimestamp(created)
	expires, err := Timestamp(t.Add(ttl))
	if err != nil {
		return Window{}, reject(ExpiryInvalid)
	}
	return Window{CreatedAt: created, ExpiresAt: expires}, nil
}

func (w Window) Validate() error {
	created, err := ParseTimestamp(w.CreatedAt)
	if err != nil {
		return err
	}
	expires, err := ParseTimestamp(w.ExpiresAt)
	if err != nil {
		return err
	}
	ttl := expires.Sub(created)
	if ttl <= 0 || ttl > MaxTTL {
		return reject(ExpiryInvalid)
	}
	return nil
}

func (w Window) TTL() (time.Duration, error) {
	if err := w.Validate(); err != nil {
		return 0, err
	}
	created, _ := ParseTimestamp(w.CreatedAt)
	expires, _ := ParseTimestamp(w.ExpiresAt)
	return expires.Sub(created), nil
}

// CheckAllocation is an additional gate, never dispatch permission. Call it
// before preparing/claiming and again immediately before any allocating send.
// Inspection never invokes this gate. Even identical explicit replay overrides
// are forbidden; changed config defaults are ignored for an existing request.
func CheckAllocation(planSchema int, w Window, now time.Time, hasOverrides bool) error {
	if hasOverrides {
		return reject(ReplayOverride)
	}
	if planSchema == 1 && w.ExpiresAt == "" {
		return reject(LegacyRequest)
	}
	if planSchema != LaunchPlanSchema {
		return reject(ExpiryInvalid)
	}
	if err := w.Validate(); err != nil {
		return err
	}
	if _, err := Timestamp(now); err != nil {
		return err
	}
	expires, _ := ParseTimestamp(w.ExpiresAt)
	if !now.UTC().Round(0).Before(expires) {
		return reject(RequestExpired)
	}
	return nil
}

func (w Window) String() string {
	ttl, err := w.TTL()
	if err != nil {
		return "expiry_invalid"
	}
	return fmt.Sprintf("ttl=%s expires_at=%s", ttl, w.ExpiresAt)
}

// ValidatePlanFields validates expiry fields without consulting today's clock.
// It is suitable for historic replay/inspection and is separate from the
// allocation gate. Other launch pins and tags remain the lifecycle validator's
// responsibility. Schema 1 must retain its original non-expiry serialization.
// Decoders must additionally reject a present expires_at field on schema 1,
// including explicit empty/null values that a Go string cannot distinguish.
func ValidatePlanFields(schema int, w Window, creationTags map[string]string) error {
	tagged, present := creationTags[TagKey]
	if schema == 1 {
		if w.ExpiresAt != "" || present {
			return reject(ExpiryInvalid)
		}
		return nil
	}
	if schema != LaunchPlanSchema {
		return reject(ExpiryInvalid)
	}
	if err := w.Validate(); err != nil {
		return err
	}
	if !present || tagged != w.ExpiresAt || creationTags["CreatedAt"] != w.CreatedAt {
		return reject(ExpiryInvalid)
	}
	return nil
}
