package lifecycle

import (
	"sort"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

// Untrusted cache fallbacks preserve only validated identifiers. They cannot
// advertise local plan pins, arbitrary diagnostics or terminal states as shared
// authority, even on error paths where allocation is already prohibited.
func unverifiedRecoveryEvidence(id string, receipts ...BatchReceipt) LaunchObservation {
	out := LaunchObservation{Receipt: BatchReceipt{RequestID: id, Attempts: []AttemptReceipt{}}, Workers: []WorkerOutcome{},
		Errors: []ResourceError{{Code: "launch_evidence_unverified", Message: "Shared launch authority could not be verified; retain these IDs for inspection without allocating again."}}}
	attempts := map[string]int{}
	workers := map[string]WorkerOutcome{}
	for _, receipt := range receipts {
		for _, a := range receipt.Attempts {
			if !ValidRequest(a.AttemptID) {
				continue
			}
			n, exists := attempts[a.AttemptID]
			if !exists {
				n = len(out.Receipt.Attempts)
				attempts[a.AttemptID] = n
				known := AttemptReceipt{AttemptID: a.AttemptID, State: "unknown", InstanceIDs: []string{}, Errors: []ResourceError{}}
				if ValidRequest(a.ParentID) {
					known.ParentID = a.ParentID
				}
				if fleetIDRE.MatchString(a.FleetID) {
					known.FleetID = a.FleetID
				}
				out.Receipt.Attempts = append(out.Receipt.Attempts, known)
			}
			out.Receipt.Attempts[n].InstanceIDs = unionInstanceIDs(out.Receipt.Attempts[n].InstanceIDs, a.InstanceIDs)
			for _, workerID := range out.Receipt.Attempts[n].InstanceIDs {
				workers[workerID] = WorkerOutcome{Instance: Instance{ID: workerID, RequestID: id, State: "unknown", SSM: "not_observed", Bootstrap: "not_observed", Readiness: "not_observed", RootDeletion: "unavailable", Volumes: []Volume{}}, AttemptID: a.AttemptID, Status: "not_observed"}
			}
		}
	}
	out.Workers = sortedBatchWorkers(workers)
	return out
}

func unionInstanceIDs(groups ...[]string) []string {
	seen := map[string]bool{}
	for _, ids := range groups {
		for _, id := range ids {
			if instanceRE.MatchString(id) {
				seen[id] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func snapshotWorkers(snapshot LaunchSnapshot) []WorkerOutcome {
	workers := map[string]WorkerOutcome{}
	for _, a := range snapshot.Receipt.Attempts {
		for _, w := range snapshot.Responses[a.AttemptID].Workers {
			workers[w.ID] = w
		}
		for _, id := range a.InstanceIDs {
			if _, found := workers[id]; !found {
				workers[id] = fleetWorker(snapshot.Receipt.Plan, a, id, config.LaunchChoice{})
			}
		}
	}
	return sortedBatchWorkers(workers)
}

func sortedBatchWorkers(workers map[string]WorkerOutcome) []WorkerOutcome {
	result := make([]WorkerOutcome, 0, len(workers))
	for _, worker := range workers {
		result = append(result, worker)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *RecoveryService) outcome(observation LaunchObservation) BatchOutcome {
	r := observation.Receipt
	out := BatchOutcome{RequestID: r.RequestID, RequestedCount: r.Plan.RequestedCount, Plan: r.Plan, Status: "allocation_unknown",
		Workers: cloneWorkers(observation.Workers), Attempts: []AttemptOutcome{}, Errors: []ResourceError{}}
	if out.Workers == nil {
		out.Workers = []WorkerOutcome{}
	}
	current := map[string]WorkerOutcome{}
	for _, worker := range out.Workers {
		current[worker.ID] = worker
		if worker.Readiness == "ready" {
			out.ReadyCount++
		}
	}
	fulfilled := map[string]bool{}
	for _, a := range r.Attempts {
		ao := AttemptOutcome{AttemptID: a.AttemptID, ParentID: a.ParentID, FleetID: a.FleetID, Status: a.State, RequestedCount: a.RequestedCount,
			InstanceIDs: append([]string{}, a.InstanceIDs...), Errors: append([]ResourceError{}, a.Errors...)}
		for _, id := range a.InstanceIDs {
			worker := current[id]
			// Receipt terminal states are reconstructed from authoritative shared
			// responses. A worker disappearing later never erases that history.
			if a.State == "complete" || observation.HistoricalFulfillment[id] == a.AttemptID || (worker.Status == "allocated" && worker.AttemptID == a.AttemptID) {
				if !fulfilled[id] {
					fulfilled[id] = true
					ao.FulfilledCount++
				}
			}
		}
		if observation.Bounded && (a.State == "complete" || a.State == "rejected") {
			missing := a.RequestedCount - len(a.InstanceIDs)
			ao.MissingCount = &missing
		}
		out.Attempts = append(out.Attempts, ao)
		out.Errors = append(out.Errors, a.Errors...)
	}
	out.Errors = append(out.Errors, observation.Errors...)
	out.FulfilledCount = len(fulfilled)
	if (len(r.Attempts) == 0 && r.Plan.SchemaVersion == 1) || (len(r.Attempts) > 0 && r.Attempts[len(r.Attempts)-1].State == "prepared") {
		out.Status = "prepared"
	} else if observation.Bounded {
		missing := r.Plan.RequestedCount - len(fulfilled)
		out.MissingCount = &missing
		switch {
		case missing == 0:
			out.Status = "allocated"
		case len(fulfilled) == 0:
			out.Status = "no_capacity"
		default:
			out.Status = "partial_capacity"
		}
	}
	prefix := s.CommandPrefix
	if prefix == "" {
		prefix = "devbox"
	}
	if ValidRequest(r.RequestID) {
		out.ResumeCommand = prefix + " up --resume " + r.RequestID
		if out.MissingCount != nil && *out.MissingCount > 0 && len(r.Attempts) > 0 {
			out.RetryCommand = prefix + " up --retry-missing " + r.RequestID + " --after " + r.Attempts[len(r.Attempts)-1].AttemptID
		}
	}
	return out
}
