package expiry

import (
	"context"
	"regexp"
	"time"
)

// Scope comes from trusted local configuration or deployment environment, never
// the schedule payload. Account/region evidence on Resource must be supplied by
// an adapter after verifying STS identity and the regional endpoint/reservation.
type Scope struct {
	Account    string `json:"account"`
	Region     string `json:"region"`
	Deployment string `json:"deployment"`
	Owner      string `json:"owner"`
}

var (
	accountRE  = regexp.MustCompile(`^[0-9]{12}$`)
	regionRE   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	labelRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	instanceRE = regexp.MustCompile(`^i-([0-9a-f]{8}|[0-9a-f]{17})$`)
)

func (s Scope) Validate() error {
	if !accountRE.MatchString(s.Account) || !regionRE.MatchString(s.Region) || !labelRE.MatchString(s.Deployment) || !labelRE.MatchString(s.Owner) {
		return reject(ScopeInvalid)
	}
	return nil
}

// Tag preserves nil and duplicate AWS tag fields; never normalize into a map
// before validation. Tags need not include launch-only metadata for cleanup.
type Tag struct{ Key, Value *string }
type Resource struct {
	ID      string
	Account string
	Region  string
	State   string
	Tags    []Tag
}

// InspectTags is also usable by ls: invalid expiry is diagnostic information,
// not a reason to hide a resource or block explicit down.
func InspectTags(tags []Tag) (*string, Reason) {
	count := 0
	var value *string
	for _, tag := range tags {
		if tag.Key != nil && *tag.Key == TagKey {
			count++
			value = tag.Value
		}
	}
	if count > 1 {
		return nil, ExpiryDuplicate
	}
	if count == 0 {
		return nil, ExpiryMissing
	}
	if value == nil {
		return nil, ExpiryInvalid
	}
	if _, err := ParseTimestamp(*value); err != nil {
		return nil, ExpiryInvalid
	}
	copy := *value
	return &copy, ""
}

// Decision is a point-in-time observation, not a transferable authorization.
// ExpiresAt is null when absent/invalid; raw arbitrary tags are never emitted.
type Decision struct {
	InstanceID  string  `json:"instance_id"`
	State       string  `json:"ec2_state"`
	ExpiresAt   *string `json:"expires_at"`
	EvaluatedAt string  `json:"evaluated_at"`
	Eligible    bool    `json:"eligible"`
	Reason      Reason  `json:"reason"`
}

func Evaluate(scope Scope, resource Resource, now time.Time) Decision {
	d := Decision{InstanceID: resource.ID, State: resource.State}
	var err error
	d.EvaluatedAt, err = Timestamp(now)
	if err != nil {
		d.Reason = ClockInvalid
		return d
	}
	if scope.Validate() != nil {
		d.Reason = ScopeInvalid
		return d
	}
	if !instanceRE.MatchString(resource.ID) {
		d.Reason = ResourceInvalid
		return d
	}
	if resource.Account != scope.Account || resource.Region != scope.Region {
		d.Reason = ScopeMismatch
		return d
	}
	tags := make(map[string]string, len(resource.Tags))
	// Reject ambiguous scope before inspecting expiry, independent of tag order.
	for _, tag := range resource.Tags {
		if tag.Key == nil || *tag.Key == "" {
			d.Reason = TagsInvalid
			return d
		}
		if *tag.Key == TagKey {
			continue
		}
		if tag.Value == nil {
			d.Reason = TagsInvalid
			return d
		}
		if _, exists := tags[*tag.Key]; exists {
			d.Reason = TagsDuplicate
			return d
		}
		tags[*tag.Key] = *tag.Value
	}
	if tags["ManagedBy"] != "devbox" || tags["Deployment"] != scope.Deployment || tags["Owner"] != scope.Owner {
		d.Reason = ScopeMismatch
		return d
	}
	d.ExpiresAt, d.Reason = InspectTags(resource.Tags)
	if d.Reason != "" {
		return d
	}
	expires, _ := ParseTimestamp(*d.ExpiresAt)
	if now.UTC().Round(0).Before(expires) {
		d.Reason = ExpiryFuture
		return d
	}
	switch resource.State {
	case "pending", "running", "stopping", "stopped":
		d.Eligible, d.Reason = true, Expired
	case "shutting-down":
		d.Reason = AlreadyTerminating
	case "terminated":
		d.Reason = AlreadyTerminated
	default:
		d.Reason = StateUnknown
	}
	return d
}

// Recheck binds fresh scope/expiry/state evidence to a previously eligible ID.
// Any expiry change requires a new scan, even if the replacement is also past.
// Callers must retain Scope and the initial Decision privately and verify that
// the exact-ID response is complete, singular, and authentic before this call.
func Recheck(scope Scope, selected Decision, current Resource, now time.Time) Decision {
	d := Evaluate(scope, current, now)
	if !selected.Eligible || selected.Reason != Expired || selected.ExpiresAt == nil || selected.InstanceID != current.ID {
		d.Eligible, d.Reason = false, ResourceChanged
		return d
	}
	if d.ExpiresAt != nil && *selected.ExpiresAt != *d.ExpiresAt {
		d.Eligible, d.Reason = false, ExpiryChanged
	}
	return d
}

// Problem uses allowlisted messages; adapters must not emit raw SDK errors.
type Problem struct {
	ResourceID string `json:"resource_id,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type Volume struct {
	ID                  string `json:"volume_id"`
	Device              string `json:"device"`
	Root                bool   `json:"root"`
	DeleteOnTermination *bool  `json:"delete_on_termination"` // nil: not verified
	Deletion            string `json:"deletion"`              // not_observed, deleted, retained, unavailable
}

type Outcome struct {
	Decision
	Status       string    `json:"status"`
	RootDeletion string    `json:"root_volume_deletion"`
	Volumes      []Volume  `json:"volumes"`
	Errors       []Problem `json:"errors"`
}

// Result is shared by manual and scheduled adapters. Arrays are always present
// (empty arrays, never null). Counts are per distinct instance ID; diagnostics
// about malformed identity need not count toward scanned_count.
type Result struct {
	SchemaVersion   int       `json:"schema_version"` // 1
	Command         string    `json:"command"`        // cleanup
	OK              bool      `json:"ok"`
	ExitCode        int       `json:"exit_code"`
	Code            string    `json:"code"`
	Message         string    `json:"message"`
	Scope           Scope     `json:"scope"`
	DryRun          bool      `json:"dry_run"`
	EvaluatedAt     string    `json:"evaluated_at"`
	CompletedAt     string    `json:"completed_at"`
	ScanComplete    bool      `json:"scan_complete"`
	Complete        bool      `json:"complete"`
	ScannedCount    int       `json:"scanned_count"`
	CandidateCount  int       `json:"candidate_count"`
	TerminatedCount int       `json:"terminated_count"`
	CleanedCount    int       `json:"cleaned_count"`
	Instances       []Outcome `json:"instances"`
	Errors          []Problem `json:"errors"`
}

// Event records exact mappings before mutation, followed by outcome updates.
// A sink failure before dispatch must prevent that instance's termination;
// after dispatch it is reported as incomplete evidence, never undone/retried as
// a new allocation. The scheduler's sink must preserve events off the worker.
type Event struct {
	SchemaVersion int      `json:"schema_version"` // 1
	RunID         string   `json:"run_id"`
	Kind          string   `json:"kind"` // decision, termination_prepared, outcome, summary
	Scope         Scope    `json:"scope"`
	EmittedAt     string   `json:"emitted_at"`
	Instance      *Outcome `json:"instance,omitempty"`
	Summary       *Result  `json:"summary,omitempty"`
}

// Emit must honor context, support concurrent calls, and synchronously acknowledge
// a copied snapshot before returning nil. Callers give each event immutable
// owned data and must not hold orchestration locks while invoking the sink.
type Sink interface {
	Emit(context.Context, Event) error
}

// ScheduledInput carries correlation only; adapters strictly reject unknown
// fields. None of these values is resource scope, clock, or dispatch authority.
type ScheduledInput struct {
	SchemaVersion int    `json:"schema_version"`
	ScheduleARN   string `json:"schedule_arn,omitempty"`
	ScheduledTime string `json:"scheduled_time,omitempty"`
	ExecutionID   string `json:"execution_id,omitempty"`
	AttemptNumber string `json:"attempt_number,omitempty"`
}
