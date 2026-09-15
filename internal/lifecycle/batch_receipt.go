package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"time"
)

// BatchReceipt is a local cache of an immutable shared launch ledger. It never
// grants dispatch permission: only a positively acknowledged conditional write
// of the attempt's permanent S3 dispatch claim can grant that permission.
// Legacy Receipt remains schema 1 and uses its existing recovery path.
type BatchReceipt struct {
	SchemaVersion int              `json:"schema_version"`
	RequestID     string           `json:"request_id"`
	Plan          LaunchPlan       `json:"plan"`
	PlanSHA256    string           `json:"plan_sha256"`
	Attempts      []AttemptReceipt `json:"attempts"`
}

type AttemptReceipt struct {
	AttemptID      string          `json:"attempt_id"`
	ParentID       string          `json:"parent_attempt_id,omitempty"`
	ClientToken    string          `json:"client_token"`
	RequestedCount int             `json:"requested_count"`
	CreatedAt      string          `json:"created_at"`
	State          string          `json:"state"` // prepared, dispatched, unknown, complete, rejected
	FleetID        string          `json:"fleet_id,omitempty"`
	InstanceIDs    []string        `json:"instance_ids"`
	Errors         []ResourceError `json:"errors"`
}

// DispatchClaim is written with If-None-Match:* and is never released, expired
// or rewritten. A recovered claim (including one's own) cannot authorize send.
type DispatchClaim struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	AttemptID     string `json:"attempt_id"`
	PlanSHA256    string `json:"plan_sha256"`
	InputSHA256   string `json:"input_sha256"`
	ClientToken   string `json:"client_token"`
}

func attemptToken(planHash, attemptID string, count int) string {
	b, _ := json.Marshal(struct {
		PlanHash  string `json:"plan_sha256"`
		AttemptID string `json:"attempt_id"`
		Count     int    `json:"count"`
	}{planHash, attemptID, count})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func decodeBatchReceipt(data []byte) (BatchReceipt, error) {
	var r BatchReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return r, failure("receipt_invalid", "invalid batch receipt JSON; restore the shared launch records; ls and down remain available")
	}
	return r, r.Validate()
}

// Validate verifies lineage and count invariants, not cloud authenticity. The
// recovery service must compare every cached record against the shared ledger.
func (r BatchReceipt) Validate() error {
	invalid := failure("receipt_invalid", "batch receipt pins, lineage or fulfillment are invalid; reconcile shared launch records before any allocation")
	if !validBatchVersion(r.SchemaVersion, r.Plan.SchemaVersion) || !ValidRequest(r.RequestID) || r.Plan.RequestID != r.RequestID || r.PlanSHA256 != r.Plan.Digest() || r.Plan.RequestedCount < 1 || r.Plan.RequestedCount > 100 || len(r.Attempts) == 0 || !ValidName(r.Plan.BaseName) {
		return invalid
	}
	if err := validatePlanExpiry(r.Plan); err != nil {
		return invalid
	}
	if r.Plan.Market != "spot" && r.Plan.Market != "on-demand" {
		return invalid
	}
	fulfilled := map[string]bool{}
	fleets := map[string]bool{}
	parent := ""
	for n, attempt := range r.Attempts {
		want, err := AttemptID(r.RequestID, parent)
		_, dateErr := time.Parse(time.RFC3339Nano, attempt.CreatedAt)
		if err != nil || dateErr != nil || attempt.AttemptID != want || attempt.ParentID != parent || attempt.RequestedCount != r.Plan.RequestedCount-len(fulfilled) || attempt.RequestedCount < 1 || attempt.ClientToken != attemptToken(r.PlanSHA256, want, attempt.RequestedCount) {
			return invalid
		}
		if attempt.State != "prepared" && attempt.State != "dispatched" && attempt.State != "unknown" && attempt.State != "complete" && attempt.State != "rejected" {
			return invalid
		}
		if (attempt.State == "prepared" || attempt.State == "rejected") && (len(attempt.InstanceIDs) > 0 || attempt.FleetID != "") {
			return invalid
		}
		if attempt.FleetID != "" {
			if !fleetIDRE.MatchString(attempt.FleetID) || (fleets[attempt.FleetID] && attempt.State != "unknown") {
				return invalid
			}
			fleets[attempt.FleetID] = true
		}
		// Contradictory responses may expose more identities than requested.
		// Retain all of them as unknown evidence; they cannot have a successor.
		if (len(attempt.InstanceIDs) > attempt.RequestedCount && attempt.State != "unknown") || (attempt.State == "complete" && (attempt.FleetID == "" || attempt.InstanceIDs == nil)) {
			return invalid
		}
		for _, id := range attempt.InstanceIDs {
			if !instanceRE.MatchString(id) || fulfilled[id] {
				return invalid
			}
			fulfilled[id] = true
		}
		if n < len(r.Attempts)-1 && attempt.State != "complete" && attempt.State != "rejected" {
			return invalid
		}
		parent = attempt.AttemptID
	}
	return nil
}

// FulfilledIDs includes historical successes, even after termination. It is
// intentionally independent of current AWS inventory and worker readiness.
func (r BatchReceipt) FulfilledIDs() []string {
	ids := map[string]bool{}
	for _, attempt := range r.Attempts {
		for _, id := range attempt.InstanceIDs {
			ids[id] = true
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// MissingCapacity only computes a candidate after a terminal, complete response
// for every prior attempt. Callers still require the exact shared records and a
// new conditional claim; this arithmetic never authorizes mutation by itself.
func (r BatchReceipt) MissingCapacity(after string) (int, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	last := r.Attempts[len(r.Attempts)-1]
	if !ValidRequest(after) || after != last.AttemptID {
		return 0, failure("retry_parent_mismatch", "--after must name the latest reconciled attempt; an existing successor must be resumed")
	}
	if last.State != "complete" && last.State != "rejected" {
		return 0, failure("allocation_unknown", "the original allocation is not bounded; inspect known workers and resume without requesting more capacity")
	}
	return r.Plan.RequestedCount - len(r.FulfilledIDs()), nil
}
