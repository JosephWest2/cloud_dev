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
		if a.AttemptID != b.AttemptID || (a.State != "prepared" && a.CreatedAt != b.CreatedAt) || (a.FleetID != "" && a.FleetID != b.FleetID) || (a.State != "prepared" && b.State == "prepared") {
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

// RestoreBatch installs intact shared evidence after a lost/corrupt local cache.
// It requires the request lock. Valid conflicting/legacy receipts remain in place;
// unreadable data is preserved in a private adjacent backup before replacement.
func (s Store) RestoreBatch(r BatchReceipt) error {
	if r.Validate() != nil {
		return failure("receipt_invalid", "recovered launch evidence cannot form a valid local batch cache")
	}
	b, readErr := os.ReadFile(s.Path(r.RequestID))
	if os.IsNotExist(readErr) {
		return s.SaveBatch(r)
	}
	if readErr != nil {
		return failure("receipt_unavailable", "cannot read the local receipt for shared-record recovery")
	}
	if _, err := decodeBatchReceipt(b); err == nil {
		return s.SaveBatch(r)
	}
	var version struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(b, &version) == nil && version.SchemaVersion == 1 {
		return failure("receipt_conflict", "a legacy receipt occupies this identity; preserve it and use its legacy recovery path")
	}
	backup, err := os.CreateTemp(s.Dir, r.RequestID+".corrupt-")
	if err != nil {
		return failure("receipt_unavailable", "cannot preserve the corrupt local receipt before recovery")
	}
	name := backup.Name()
	_ = backup.Close()
	if err = os.Rename(s.Path(r.RequestID), name); err != nil {
		_ = os.Remove(name)
		return failure("receipt_unavailable", "cannot preserve the corrupt local receipt before recovery")
	}
	if err = os.Chmod(name, 0600); err != nil {
		return failure("receipt_unavailable", "cannot protect the preserved corrupt receipt; shared launch records remain authoritative")
	}
	if err = syncDir(s.Dir); err != nil {
		return failure("receipt_unavailable", "cannot durably preserve the corrupt receipt; shared launch records remain authoritative")
	}
	return s.SaveBatch(r)
}
