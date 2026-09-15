package lifecycle

import (
	"context"
	"errors"
	"io"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

// Startup evidence comes only from an authorized dispatch or a validated shared
// reconciliation. A local receipt or an arbitrary worker ID cannot enter this
// path. This is observation authority, never permission for another allocation.
type batchStartup struct {
	api       FleetInventory
	service   *Service
	attempts  map[string]startupAttempt
	retryable []error
}

type startupAttempt struct {
	plan    LaunchPlan
	attempt AttemptReceipt
}

func (s *batchStartup) remember(plan LaunchPlan, attempts []AttemptReceipt) {
	if s.attempts == nil {
		s.attempts = map[string]startupAttempt{}
	}
	for _, attempt := range attempts {
		for _, id := range attempt.InstanceIDs {
			one := attempt
			one.InstanceIDs = []string{id}
			s.attempts[id] = startupAttempt{plan: plan, attempt: one}
		}
	}
}

func (s *batchStartup) eligible(plan LaunchPlan, worker WorkerOutcome) bool {
	evidence, ok := s.attempts[worker.ID]
	return ok && worker.Status == "not_observed" && evidence.attempt.AttemptID == worker.AttemptID && evidence.plan.Digest() == plan.Digest()
}

func (s *batchStartup) wait(ctx context.Context, plan LaunchPlan, worker *WorkerOutcome) error {
	if !s.eligible(plan, *worker) {
		return failure("worker_not_verified", "worker allocation identity has not been verified; resume request inspection before readiness")
	}
	evidence := s.attempts[worker.ID]
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		observed, err := VerifyFleetWorkers(ctx, s.api, evidence.plan, evidence.attempt, []WorkerOutcome{*worker}, s.service.Clock)
		if len(observed) != 1 || observed[0].ID != worker.ID {
			return failure("worker_inventory_invalid", "Exact startup inspection returned contradictory identities; preserve every known worker and root.")
		}
		*worker = observed[0]
		if err == nil {
			return nil
		}
		if !startupObservationUnavailable(err) {
			return err
		}
		if batchCannotRun(worker.State) {
			return batchNotRunning(&worker.Instance)
		}
		if err := s.service.pause(ctx, attempt); err != nil {
			return err
		}
	}
}

func startupObservationUnavailable(err error) bool {
	var f *Failure
	return errors.As(err, &f) && f.Code == "worker_observation_unavailable"
}

func (s *batchStartup) mayRefresh(err error) bool {
	for _, observed := range s.retryable {
		if err == observed {
			return true
		}
	}
	return false
}

// Reconciliation re-establishes allocation bounds from shared original response
// evidence. Successful readiness alone cannot supply a missing-capacity bound.
// Preserve the observations already obtained by independent readiness workers,
// but only carry ready status onto a currently verified identical live worker.
func refreshBatchStartup(previous, current BatchOutcome, preserveErrors bool) BatchOutcome {
	known := map[string]WorkerOutcome{}
	for _, worker := range previous.Workers {
		known[worker.ID] = worker
	}
	for n := range current.Workers {
		worker := &current.Workers[n]
		old, ok := known[worker.ID]
		if !ok {
			continue
		}
		delete(known, worker.ID)
		worker.Volumes = mergeFleetVolumes(old.Volumes, worker.Volumes)
		if old.Status == "allocated" && worker.Status == "allocated" && !batchCannotRun(worker.State) && batchReadyIdentity(old.Instance, worker.Instance) {
			worker.SSM, worker.Bootstrap, worker.Readiness = old.SSM, old.Bootstrap, old.Readiness
			worker.ObservationCode, worker.ProbeCommandID, worker.HostKey = old.ObservationCode, old.ProbeCommandID, old.HostKey
		}
	}
	for _, worker := range known {
		worker.Status = "not_observed"
		worker.Readiness, worker.HostKey = "unknown", ""
		current.Workers = append(current.Workers, worker)
	}
	if preserveErrors || len(known) > 0 {
		current.MissingCount, current.RetryCommand = nil, ""
		for n := range current.Attempts {
			current.Attempts[n].MissingCount = nil
		}
		for _, old := range previous.Errors {
			found := false
			for _, fresh := range current.Errors {
				found = found || old == fresh
			}
			if !found {
				current.Errors = append(current.Errors, old)
			}
		}
	}
	// sortedBatchWorkers also keeps the final ordering deterministic when a
	// failed refresh lacked identities learned during startup verification.
	workers := map[string]WorkerOutcome{}
	for _, worker := range current.Workers {
		workers[worker.ID] = worker
	}
	current.Workers = sortedBatchWorkers(workers)
	return current
}

func (s *batchStartup) readiness(ctx context.Context, recovery *RecoveryService, outcome *BatchOutcome, operationErr error, progress io.Writer) error {
	pending := false
	for _, worker := range outcome.Workers {
		pending = pending || s.eligible(outcome.Plan, worker)
	}
	var prepare func(context.Context, *WorkerOutcome) error
	if pending {
		prepare = func(ctx context.Context, worker *WorkerOutcome) error {
			return s.wait(ctx, outcome.Plan, worker)
		}
	}
	readyErr := s.service.waitBatchReady(ctx, config.Manifest{Readiness: outcome.Plan.Readiness}, outcome.Workers, progress, prepare)
	if pending && ctx.Err() == nil {
		current, refreshErr := recovery.Resume(ctx, outcome.RequestID)
		refreshable := s.mayRefresh(operationErr)
		if supportedPlan(current.Plan.SchemaVersion) && current.Plan.Digest() == outcome.Plan.Digest() {
			*outcome = refreshBatchStartup(*outcome, current, operationErr != nil && !refreshable)
		}
		if refreshable {
			operationErr = nil
		}
		operationErr = errors.Join(operationErr, refreshErr)
	}
	return errors.Join(operationErr, readyErr)
}
