package lifecycle

import (
	"context"
	"errors"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
)

// RecoveryService reads shared authority before using local evidence. Observation
// never invokes Dispatcher, including for a prepared but unclaimed batch.
type RecoveryService struct {
	Clock      expiry.Clock
	Scope      config.Config
	Ledger     RecoveryLedger
	Cache      Store
	Observe    func(context.Context, LaunchSnapshot) (LaunchObservation, error)
	Dispatcher *AttemptService
	// CommandPrefix preserves explicit config and AWS profile in recovery hints.
	CommandPrefix string
}

func (s *RecoveryService) Resume(ctx context.Context, id string) (BatchOutcome, error) {
	if !ValidRequest(id) {
		return BatchOutcome{}, failure("request_invalid", "resume requires the original 32-character request ID")
	}
	unlock, err := s.Cache.Lock(ctx, id)
	if err != nil {
		return BatchOutcome{RequestID: id}, err
	}
	defer unlock()
	_, observation, err := s.observe(ctx, id)
	return s.outcome(observation), err
}

// observe requires the local request lock. Shared evidence is authoritative;
// cached unknown IDs merely request additional exact inspection.
func (s *RecoveryService) observe(ctx context.Context, id string) (LaunchSnapshot, LaunchObservation, error) {
	empty := LaunchObservation{Receipt: BatchReceipt{RequestID: id}, Workers: []WorkerOutcome{}, Errors: []ResourceError{}}
	if s.Ledger == nil || s.Observe == nil {
		return LaunchSnapshot{}, empty, failure("recovery_unavailable", "shared launch recovery and AWS inspection are required")
	}
	cached, cacheErr := s.Cache.LoadBatch(id)
	if cacheErr == nil && !batchScopeMatches(cached.Plan, s.Scope) {
		return LaunchSnapshot{}, unverifiedRecoveryEvidence(id, cached), failure("scope_mismatch", "the local request belongs to a different account, region, deployment or owner; select its original configuration")
	}
	snapshot, loadErr := s.Ledger.Load(ctx, id)
	if snapshot.Receipt.RequestID == "" {
		snapshot.Receipt.RequestID = id
	}
	if snapshot.Receipt.Plan.SchemaVersion != 0 && !batchScopeMatches(snapshot.Receipt.Plan, s.Scope) {
		return snapshot, unverifiedRecoveryEvidence(id, cached, snapshot.Receipt), failure("scope_mismatch", "shared launch records differ from the selected account, region, deployment or owner")
	}
	if cacheErr == nil && snapshot.Receipt.PlanSHA256 != "" && cached.PlanSHA256 != snapshot.Receipt.PlanSHA256 {
		return snapshot, unverifiedRecoveryEvidence(id, cached, snapshot.Receipt), failure("receipt_conflict", "local and shared immutable launch plans differ; preserve both and inspect the original request")
	}
	if cacheErr == nil && snapshot.Receipt.PlanSHA256 == cached.PlanSHA256 {
		mergeUnknownCache(&snapshot, cached)
	}
	if loadErr != nil {
		// Even corrupt or incomplete shared history can contain known identities.
		// It cannot authorize allocation, but retain those IDs in the result.
		observation := LaunchObservation{Receipt: snapshot.Receipt, Workers: snapshotWorkers(snapshot), Errors: []ResourceError{}}
		if cacheErr == nil {
			if observation.Receipt.Plan.SchemaVersion == 0 {
				observation = unverifiedRecoveryEvidence(id, cached)
			}
			known := map[string]WorkerOutcome{}
			for _, w := range observation.Workers {
				known[w.ID] = w
			}
			for _, a := range cached.Attempts {
				for _, workerID := range a.InstanceIDs {
					if _, exists := known[workerID]; !exists {
						known[workerID] = fleetWorker(cached.Plan, a, workerID, config.LaunchChoice{})
					}
				}
			}
			observation.Workers = sortedBatchWorkers(known)
		}
		return snapshot, observation, loadErr
	}
	if len(snapshot.Receipt.Attempts) == 0 {
		return snapshot, LaunchObservation{Receipt: snapshot.Receipt, Workers: []WorkerOutcome{}, Errors: []ResourceError{}}, nil
	}
	if cacheErr == nil && cachedIdentityConflict(snapshot, cached) {
		workers := map[string]WorkerOutcome{}
		for _, w := range snapshotWorkers(snapshot) {
			workers[w.ID] = w
		}
		for _, a := range cached.Attempts {
			for _, workerID := range a.InstanceIDs {
				if _, exists := workers[workerID]; !exists {
					workers[workerID] = fleetWorker(cached.Plan, a, workerID, config.LaunchChoice{})
				}
			}
		}
		return snapshot, LaunchObservation{Receipt: snapshot.Receipt, Workers: sortedBatchWorkers(workers)},
			failure("receipt_conflict", "local identities contradict the complete shared response; preserve every known ID and inspect before retrying")
	}
	observation, observeErr := s.Observe(ctx, snapshot)
	if observation.Receipt.RequestID == "" {
		observation.Receipt = snapshot.Receipt
	}
	if observeErr != nil {
		observation.Bounded = false
	}
	// A failed read never turns local cache writes into shared authority.
	// Preserve inspectable evidence even when the observation itself failed.
	saveErr := s.Cache.RestoreBatch(observation.Receipt)
	if observeErr != nil {
		return snapshot, observation, observeErr
	}
	if saveErr != nil {
		observation.Bounded = false
		return snapshot, observation, saveErr
	}
	return snapshot, observation, nil
}

func cachedIdentityConflict(snapshot LaunchSnapshot, cached BatchReceipt) bool {
	for _, shared := range snapshot.Receipt.Attempts {
		for _, local := range cached.Attempts {
			if shared.AttemptID != local.AttemptID {
				continue
			}
			if shared.FleetID != "" && local.FleetID != "" && shared.FleetID != local.FleetID {
				return true
			}
			if shared.State != "complete" && shared.State != "rejected" {
				continue
			}
			known := map[string]bool{}
			for _, id := range shared.InstanceIDs {
				known[id] = true
			}
			for _, id := range local.InstanceIDs {
				if !known[id] {
					return true
				}
			}
		}
	}
	return false
}

func batchScopeMatches(p LaunchPlan, c config.Config) bool {
	return p.Account == c.ExpectedAccount && p.Region == c.Region && p.Deployment == c.Deployment && p.Owner == c.Owner
}

func mergeUnknownCache(snapshot *LaunchSnapshot, cached BatchReceipt) {
	for n, shared := range snapshot.Receipt.Attempts {
		if shared.State == "complete" || shared.State == "rejected" {
			continue
		}
		for _, local := range cached.Attempts {
			if shared.AttemptID != local.AttemptID || shared.ClientToken != local.ClientToken || shared.RequestedCount != local.RequestedCount {
				continue
			}
			// Cached terminal claims cannot establish allocation completeness.
			// Only an existing shared dispatch claim permits unknown observations.
			if _, dispatched := snapshot.Claims[shared.AttemptID]; !dispatched {
				continue
			}
			if len(local.InstanceIDs) > 0 || local.FleetID != "" {
				snapshot.Receipt.Attempts[n].State = "unknown"
				snapshot.Receipt.Attempts[n].InstanceIDs = unionInstanceIDs(shared.InstanceIDs, local.InstanceIDs)
				if shared.FleetID == "" {
					snapshot.Receipt.Attempts[n].FleetID = local.FleetID
				}
			}
		}
	}
}

// RetryMissing explicitly authorizes one deterministic successor slot. Repeating
// an older --after only observes its existing successor; it never dispatches it.
func (s *RecoveryService) RetryMissing(ctx context.Context, m config.Manifest, p config.Profile, id, after string, announce func(BatchReceipt, string) error) (BatchOutcome, error) {
	if !ValidRequest(id) || !ValidRequest(after) {
		return BatchOutcome{}, failure("request_invalid", "retry requires the original request and exact parent attempt identities")
	}
	unlock, err := s.Cache.Lock(ctx, id)
	if err != nil {
		return BatchOutcome{RequestID: id}, err
	}
	snapshot, observation, err := s.observe(ctx, id)
	out := s.outcome(observation)
	if err != nil {
		unlock()
		return out, err
	}
	r := snapshot.Receipt
	parentIndex := -1
	for n, a := range r.Attempts {
		if a.AttemptID == after {
			parentIndex = n
			break
		}
	}
	if parentIndex < 0 {
		unlock()
		return out, failure("retry_parent_mismatch", "--after must name an attempt in the original shared request")
	}
	if parentIndex < len(r.Attempts)-1 {
		unlock()
		return out, nil
	}
	if !observation.Bounded {
		unlock()
		return out, failure("allocation_unknown", "the original allocation is not bounded; resume and inspect known workers without requesting more capacity")
	}
	missing, err := r.MissingCapacity(after)
	if err != nil {
		unlock()
		return out, err
	}
	if missing == 0 {
		unlock()
		return out, nil
	}
	if err := checkAllocation(r.Plan, s.Clock); err != nil {
		unlock()
		return out, err
	}
	if err := requireExpiryManifest(m); err != nil {
		unlock()
		return out, err
	}
	maximum := s.Scope.MaxCount
	if maximum == 0 {
		maximum = config.DefaultMaxCount
	}
	if config.ValidateMaxCount(maximum) != nil || r.Plan.RequestedCount > maximum {
		unlock()
		return out, failure("count_invalid", "the original requested count exceeds current max_count; observation and cleanup remain available")
	}
	if s.Dispatcher == nil {
		unlock()
		return out, failure("allocator_unavailable", "shared recovery has no verified dispatch service")
	}
	created, err := expiry.Timestamp(clockNow(s.Clock))
	if err != nil {
		unlock()
		return out, expiryFailure(err)
	}
	aid, _ := AttemptID(id, after)
	candidate := cloneBatch(r)
	candidate.Attempts = append(candidate.Attempts, AttemptReceipt{
		AttemptID: aid, ParentID: after, ClientToken: attemptToken(r.PlanSHA256, aid, missing), RequestedCount: missing,
		CreatedAt: created, State: "prepared", InstanceIDs: []string{}, Errors: []ResourceError{},
	})
	unlock()
	// Dispatch takes the same local lock and revalidates current pins plus shared
	// lineage before its claim. Another client's intervening successor cannot
	// authorize a second send, even across independent state directories.
	dispatched, dispatchErr := s.Dispatcher.Dispatch(ctx, m, p, candidate, announce)
	if !dispatched.Dispatched && recoverablePreparationConflict(dispatchErr) {
		// A concurrent winner may already have canonical preparation/response.
		// Observe that record, including when our own prepared timestamp differed.
		current, err := s.Resume(ctx, id)
		retainBatchExpiry(&current, out)
		return current, err
	}
	combined := LaunchObservation{Receipt: dispatched.Receipt, Workers: append(cloneWorkers(observation.Workers), dispatched.Workers...), Errors: observation.Errors, HistoricalFulfillment: observation.HistoricalFulfillment}
	combined.Bounded = dispatchErr == nil && dispatched.Outcome().MissingCount != nil
	result := s.outcome(combined)
	if dispatchErr != nil {
		return result, dispatchErr
	}
	return result, nil
}

func recoverablePreparationConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrLaunchRecordConflict) {
		return true
	}
	var fail *Failure
	return errors.As(err, &fail) && (fail.Code == "receipt_conflict" || fail.Code == "allocation_unknown")
}
