package lifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/smithy-go/middleware"
)

// BatchCache is durable local evidence, never authority to send an attempt.
type BatchCache interface {
	Lock(context.Context, string) (func(), error)
	Path(string) string
	SaveBatch(BatchReceipt) error
}

// PreparedAttempt pins the exact adapter input as well as the original plan.
// It is stored separately from mutable local observation state.
type PreparedAttempt struct {
	SchemaVersion int            `json:"schema_version"`
	RequestID     string         `json:"request_id"`
	PlanSHA256    string         `json:"plan_sha256"`
	InputSHA256   string         `json:"input_sha256"`
	Attempt       AttemptReceipt `json:"attempt"`
}

// AttemptResponse preserves the original normalized API evidence. Its complete
// state describes the response envelope; recovery must still verify every known
// worker against the original pins before authorizing a missing-capacity retry.
// Observation failures must not erase or rewrite this immutable response.
type AttemptResponse struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	PlanSHA256    string          `json:"plan_sha256"`
	InputSHA256   string          `json:"input_sha256"`
	Attempt       AttemptReceipt  `json:"attempt"`
	Workers       []WorkerOutcome `json:"instances"`
}

// BatchLedger implementations must verify exact immutable shared plan/prepared
// records and prior terminal responses in Prepare. Claim returns true ONLY for
// a positively acknowledged new If-None-Match:* write. Existing records, failed
// writes (including ambiguous acknowledgments), and recovered GETs return false.
// No implementation may release or expire a claim. The S3 backend is in #31.
type BatchLedger interface {
	Prepare(context.Context, BatchReceipt, PreparedAttempt) error
	Claim(context.Context, config.LaunchLedger, DispatchClaim) (bool, error)
	RecordResponse(context.Context, config.LaunchLedger, AttemptResponse) error
}

type FleetAPI interface {
	CreateFleet(context.Context, *ec2.CreateFleetInput, ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error)
}

// AttemptService owns one permanently claimed, expiry-gated Fleet dispatch.
// Legacy Up only observes saved single-worker requests.
type AttemptService struct {
	Clock            expiry.Clock
	API              FleetAPI
	Scope            config.Config
	VerifyFoundation func(context.Context, config.Manifest, config.Profile) error
	Ledger           BatchLedger
	Cache            BatchCache
	VerifyWorkers    func(context.Context, LaunchPlan, AttemptReceipt, []WorkerOutcome) ([]WorkerOutcome, error)
}

type AttemptResult struct {
	Receipt          BatchReceipt
	Workers          []WorkerOutcome
	ReceiptPath      string
	Dispatched       bool
	ObservationError error
}

// Outcome reports verified fulfillment separately from every known identity.
// An unavailable or contradictory observation cannot authorize missing capacity.
func (r AttemptResult) Outcome() AttemptOutcome {
	if len(r.Receipt.Attempts) == 0 {
		return AttemptOutcome{InstanceIDs: []string{}, Errors: []ResourceError{}}
	}
	a := r.Receipt.Attempts[len(r.Receipt.Attempts)-1]
	out := AttemptOutcome{AttemptID: a.AttemptID, ParentID: a.ParentID, FleetID: a.FleetID, Status: a.State, RequestedCount: a.RequestedCount,
		InstanceIDs: append([]string{}, a.InstanceIDs...), Errors: append([]ResourceError{}, a.Errors...)}
	verified := map[string]bool{}
	known := map[string]bool{}
	for _, id := range a.InstanceIDs {
		known[id] = true
	}
	for _, w := range r.Workers {
		if w.Status == "allocated" && known[w.ID] && w.AttemptID == a.AttemptID {
			verified[w.ID] = true
		}
	}
	out.FulfilledCount = len(verified)
	if (a.State == "complete" || a.State == "rejected") && r.ObservationError == nil && len(verified) == len(known) && len(known) == len(a.InstanceIDs) {
		missing := a.RequestedCount - len(a.InstanceIDs)
		out.MissingCount = &missing
	}
	return out
}

// NewBatchReceipt resolves a new random request without making cloud calls.
func NewBatchReceipt(c config.Config, m config.Manifest, p config.Profile, selection LaunchSelection) (BatchReceipt, error) {
	return NewBatchReceiptWithClock(c, m, p, selection, expiry.SystemClock{})
}

func NewBatchReceiptWithClock(c config.Config, m config.Manifest, p config.Profile, selection LaunchSelection, clock expiry.Clock) (BatchReceipt, error) {
	if err := requireExpiryManifest(m); err != nil {
		return BatchReceipt{}, err
	}
	created, err := expiry.Timestamp(clockNow(clock))
	if err != nil {
		return BatchReceipt{}, expiryFailure(err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return BatchReceipt{}, failure("receipt_unavailable", "cannot generate a launch request identity; no allocation performed")
	}
	plan, err := BuildLaunchPlan(c, m, p, selection, hex.EncodeToString(random[:]), created)
	if err != nil {
		return BatchReceipt{}, err
	}
	id, _ := AttemptID(plan.RequestID, "")
	r := BatchReceipt{SchemaVersion: 3, RequestID: plan.RequestID, Plan: plan, PlanSHA256: plan.Digest()}
	r.Attempts = []AttemptReceipt{{AttemptID: id, ClientToken: attemptToken(r.PlanSHA256, id, plan.RequestedCount), RequestedCount: plan.RequestedCount, CreatedAt: plan.CreatedAt, State: "prepared", InstanceIDs: []string{}, Errors: []ResourceError{}}}
	return r, r.Validate()
}

func fleetInputDigest(in *ec2.CreateFleetInput) string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Clone in memory without invoking strict wire decoders: invalid input must
// remain intact for validation and known-ID error reporting, not become zero.
func cloneBatch(r BatchReceipt) BatchReceipt {
	copy := r
	copy.Plan.CreationTags = nil
	if r.Plan.CreationTags != nil {
		copy.Plan.CreationTags = make(map[string]string, len(r.Plan.CreationTags))
		for k, v := range r.Plan.CreationTags {
			copy.Plan.CreationTags[k] = v
		}
	}
	if r.Plan.Choices != nil {
		copy.Plan.Choices = append([]config.LaunchChoice{}, r.Plan.Choices...)
	}
	if r.Plan.Image.RootDisk != nil {
		disk := *r.Plan.Image.RootDisk
		copy.Plan.Image.RootDisk = &disk
	}
	if r.Attempts != nil {
		copy.Attempts = make([]AttemptReceipt, len(r.Attempts))
		for n, a := range r.Attempts {
			copy.Attempts[n] = a
			if a.InstanceIDs != nil {
				copy.Attempts[n].InstanceIDs = append([]string{}, a.InstanceIDs...)
			}
			if a.Errors != nil {
				copy.Attempts[n].Errors = append([]ResourceError{}, a.Errors...)
			}
		}
	}
	return copy
}

func cloneWorkers(workers []WorkerOutcome) []WorkerOutcome {
	b, _ := json.Marshal(workers)
	var copy []WorkerOutcome
	_ = json.Unmarshal(b, &copy)
	// Preserve same-run diagnostic provenance across outcome copies, while the
	// permanent JSON representation remains unchanged and cannot assert it.
	for n := range copy {
		copy[n].expiryObserved = workers[n].expiryObserved
	}
	return copy
}

// Dispatch must only be called for an explicitly authorized new initial or
// missing-capacity attempt. Batch resume never calls it, even when prepared.
func (s *AttemptService) Dispatch(ctx context.Context, m config.Manifest, p config.Profile, receipt BatchReceipt, announce func(BatchReceipt, string) error) (AttemptResult, error) {
	r := cloneBatch(receipt)
	result := AttemptResult{Receipt: r, Workers: []WorkerOutcome{}}
	if err := r.Validate(); err != nil {
		return result, err
	}
	if err := checkAllocation(r.Plan, s.Clock); err != nil {
		return result, err
	}
	if err := requireExpiryManifest(m); err != nil {
		return result, err
	}
	n := len(r.Attempts) - 1
	a := r.Attempts[n]
	if a.State != "prepared" {
		return result, failure("attempt_already_dispatched", "this attempt is observation-only; resume its request without allocating again")
	}
	// Reconstruct from trusted current config/manifest/profile before any writes
	// or allocation. Even a valid local receipt is not a trusted launch source.
	plan, err := buildLaunchPlan(s.Scope, m, p, LaunchSelection{Profile: r.Plan.Profile, Name: r.Plan.BaseName, Group: r.Plan.Group, Count: r.Plan.RequestedCount, OnDemand: r.Plan.Market == "on-demand"}, r.RequestID, r.Plan.CreatedAt, r.Plan.ExpiresAt, r.Plan.SchemaVersion)
	if err != nil {
		return result, err
	}
	if plan.Digest() != r.PlanSHA256 {
		return result, failure("replay_parameters_changed", "current foundation or profile changes this request; restore its original pins before allocating missing capacity")
	}
	input, err := BuildFleetInput(r.Plan, a)
	if err != nil {
		return result, err
	}
	if s.API == nil || s.VerifyFoundation == nil || s.Cache == nil || s.Ledger == nil || s.VerifyWorkers == nil || announce == nil {
		return result, failure("allocator_unavailable", "durable allocation and verification services are required before dispatch")
	}
	result.ReceiptPath = s.Cache.Path(r.RequestID)
	unlock, err := s.Cache.Lock(ctx, r.RequestID)
	if err != nil {
		return result, err
	}
	defer unlock()
	if err = s.VerifyFoundation(ctx, m, p); err != nil {
		return result, err
	}
	if err := checkAllocation(r.Plan, s.Clock); err != nil {
		return result, err
	}
	if err = s.Cache.SaveBatch(r); err != nil {
		return result, err
	}
	// The callback receives an isolated copy, so output hooks cannot change the
	// plan or token after validation. Failure stops before any shared mutation.
	if err = announce(cloneBatch(r), result.ReceiptPath); err != nil {
		return result, failure("output_unavailable", "cannot announce durable request recovery; no allocation performed")
	}
	if err := checkAllocation(r.Plan, s.Clock); err != nil {
		return result, err
	}
	prepared := PreparedAttempt{SchemaVersion: 1, RequestID: r.RequestID, PlanSHA256: r.PlanSHA256, InputSHA256: fleetInputDigest(input), Attempt: a}
	if err = s.Ledger.Prepare(ctx, cloneBatch(r), prepared); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	r.Attempts[n].State = "dispatched"
	result.Receipt = cloneBatch(r)
	if err = s.Cache.SaveBatch(r); err != nil {
		return result, err
	}
	if err := checkAllocation(r.Plan, s.Clock); err != nil {
		return result, err
	}
	claim := DispatchClaim{SchemaVersion: 1, RequestID: r.RequestID, AttemptID: a.AttemptID, PlanSHA256: r.PlanSHA256, InputSHA256: prepared.InputSHA256, ClientToken: a.ClientToken}
	won, claimErr := s.Ledger.Claim(ctx, r.Plan.LaunchLedger, claim)
	if claimErr != nil || !won {
		return result, failure("allocation_unknown", "shared dispatch permission was not positively acknowledged; resume or inspect this request without allocating again")
	}
	// A crash or cancellation after claiming intentionally leaves uncertainty.
	// The permanent claim is never released, even if no call reached EC2.
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err := checkAllocation(r.Plan, s.Clock); err != nil {
		return result, err
	}
	result.Dispatched = true
	var sdkAttempts int
	out, callErr := s.API.CreateFleet(ctx, input, func(o *ec2.Options) {
		o.Retryer = retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 1 })
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			if err := stack.Finalize.Add(middleware.FinalizeMiddlewareFunc("CheckFleetExpiry", func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
				if err := checkAllocation(r.Plan, s.Clock); err != nil {
					return middleware.FinalizeOutput{}, middleware.Metadata{}, err
				}
				return next.HandleFinalize(ctx, in)
			}), middleware.After); err != nil {
				return err
			}
			return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("CaptureFleetAttemptEvidence", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
				out, meta, err := next.HandleInitialize(ctx, in)
				if attempts, ok := retry.GetAttemptResults(meta); ok {
					sdkAttempts = len(attempts.Results)
				}
				return out, meta, err
			}), middleware.Before)
		})
	})
	observed, workers := NormalizeFleetResponse(r.Plan, a, out, callErr)
	// A later permission/validation error cannot prove zero after an earlier
	// potentially accepted transport attempt. Missing SDK evidence also fails
	// closed; only one observed SDK attempt permits a definitive rejection.
	if observed.State == "rejected" && sdkAttempts != 1 {
		observed.State = "unknown"
	}
	r.Attempts[n] = observed
	result.Receipt = cloneBatch(r)
	result.Workers = workers
	response := AttemptResponse{SchemaVersion: 1, RequestID: r.RequestID, PlanSHA256: r.PlanSHA256, InputSHA256: prepared.InputSHA256, Attempt: observed, Workers: cloneWorkers(workers)}
	// Attempt both persistence paths even if one fails. Preserve the original
	// response before eventual-consistency observation or caller cancellation.
	cacheErr := s.Cache.SaveBatch(r)
	responseErr := s.Ledger.RecordResponse(ctx, r.Plan.LaunchLedger, response)
	if len(workers) > 0 {
		result.Workers, result.ObservationError = s.VerifyWorkers(ctx, r.Plan, observed, workers)
	}
	if cacheErr != nil {
		return result, cacheErr
	}
	if responseErr != nil {
		return result, responseErr
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if result.ObservationError != nil {
		return result, result.ObservationError
	}
	var expiryErr *Failure
	if errors.As(callErr, &expiryErr) && (expiryErr.Code == "request_expired" || expiryErr.Code == "clock_invalid") {
		return result, expiryErr
	}
	if observed.State == "unknown" {
		return result, failure("allocation_unknown", "allocation could not be bounded; resume this request and inspect every known worker before any new allocation")
	}
	return result, nil
}
