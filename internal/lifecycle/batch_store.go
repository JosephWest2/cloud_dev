package lifecycle

import (
	"encoding/json"
	"os"
	"reflect"
)

func (s Store) LoadBatch(id string) (BatchReceipt, error) {
	if !ValidRequest(id) {
		return BatchReceipt{}, failure("request_invalid", "batch recovery requires a valid request identity")
	}
	b, err := os.ReadFile(s.Path(id))
	if err != nil {
		return BatchReceipt{}, failure("receipt_unavailable", "cannot read the local batch cache; restore shared launch records; inventory and cleanup remain available")
	}
	r, err := decodeBatchReceipt(b)
	if err == nil && r.RequestID != id {
		err = failure("receipt_invalid", "local batch cache does not match its request identity")
	}
	return r, err
}

// SaveBatch requires the request lock. It will not overwrite a legacy receipt,
// erase known IDs, change immutable pins, or reset dispatch evidence to prepared.
func (s Store) SaveBatch(r BatchReceipt) error {
	fail := failure("receipt_unavailable", "cannot durably save the batch cache; retain the reported request, Fleet and worker IDs and reconcile shared records")
	if r.Validate() != nil {
		return fail
	}
	if b, err := os.ReadFile(s.Path(r.RequestID)); err == nil {
		old, decodeErr := decodeBatchReceipt(b)
		if decodeErr != nil || !batchCacheCanAdvance(old, r) {
			return failure("receipt_conflict", "local receipt conflicts with shared launch evidence; preserve it and reconcile before allocation")
		}
	} else if !os.IsNotExist(err) {
		return fail
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fail
	}
	f, err := os.CreateTemp(s.Dir, ".batch-")
	if err != nil {
		return fail
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return fail
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return fail
	}
	if err = f.Close(); err != nil {
		return fail
	}
	if err = os.Rename(f.Name(), s.Path(r.RequestID)); err != nil {
		return fail
	}
	if syncDir(s.Dir) != nil {
		return fail
	}
	return nil
}

func batchCacheCanAdvance(old, next BatchReceipt) bool {
	if old.RequestID != next.RequestID || old.PlanSHA256 != next.PlanSHA256 || len(next.Attempts) < len(old.Attempts) {
		return false
	}
	for n, a := range old.Attempts {
		b := next.Attempts[n]
		if a.AttemptID != b.AttemptID || a.CreatedAt != b.CreatedAt || (a.FleetID != "" && a.FleetID != b.FleetID) || (a.State != "prepared" && b.State == "prepared") {
			return false
		}
		if (a.State == "complete" || a.State == "rejected") && !reflect.DeepEqual(a, b) {
			return false
		}
		if a.State == "unknown" && b.State == "dispatched" {
			return false
		}
		ids := map[string]bool{}
		for _, id := range b.InstanceIDs {
			ids[id] = true
		}
		for _, id := range a.InstanceIDs {
			if !ids[id] {
				return false
			}
		}
	}
	return true
}
