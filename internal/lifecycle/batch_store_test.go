package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestBatchCacheRetainsUnknownExcessIdentities(t *testing.T) {
	r := receiptFixture(t)
	r.Attempts[0].State = "unknown"
	r.Attempts[0].InstanceIDs = []string{"i-12345678", "i-23456789", "i-34567890"}
	if err := r.Validate(); err != nil {
		t.Fatal("contradictory identities must remain recoverable:", err)
	}
	if _, err := r.MissingCapacity(r.Attempts[0].AttemptID); err == nil {
		t.Fatal("unknown excess enabled retry")
	}
	store := Store{Dir: t.TempDir()}
	unlock, err := store.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = store.SaveBatch(r); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadBatch(r.RequestID)
	if err != nil || !reflect.DeepEqual(loaded, r) {
		t.Fatal("lost unknown evidence", err)
	}
	if info, err := os.Stat(store.Path(r.RequestID)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("receipt must be private")
	}
}

func TestBatchCacheRejectsRewindsPinChangesAndLegacyOverwrite(t *testing.T) {
	for _, name := range []string{"prepared", "dispatched", "drop-id", "pins", "terminal-change", "legacy"} {
		t.Run(name, func(t *testing.T) {
			r := receiptFixture(t)
			if name != "terminal-change" {
				r.Attempts[0].State = "unknown"
			}
			store := Store{Dir: t.TempDir()}
			unlock, err := store.Lock(context.Background(), r.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			if err = store.SaveBatch(r); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "prepared":
				r.Attempts[0].State = "prepared"
				r.Attempts[0].FleetID = ""
				r.Attempts[0].InstanceIDs = []string{}
			case "dispatched":
				r.Attempts[0].State = "dispatched"
			case "drop-id":
				r.Attempts[0].InstanceIDs = []string{}
			case "pins":
				r.Plan.BaseName = "changed"
				r.Plan.CreationTags["BaseName"] = "changed"
				r.Plan.CreationTags["Name"] = "changed"
				r.PlanSHA256 = r.Plan.Digest()
				r.Attempts[0].ClientToken = attemptToken(r.PlanSHA256, r.Attempts[0].AttemptID, r.Attempts[0].RequestedCount)
			case "terminal-change":
				r.Attempts[0].InstanceIDs = append(r.Attempts[0].InstanceIDs, "i-23456789")
			case "legacy":
				b, _ := json.Marshal(Receipt{SchemaVersion: 1, RequestID: r.RequestID})
				if err = os.WriteFile(store.Path(r.RequestID), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(store.Path(r.RequestID))
			if err = store.SaveBatch(r); err == nil {
				t.Fatal("cache overwrite accepted")
			}
			after, _ := os.ReadFile(store.Path(r.RequestID))
			if string(before) != string(after) {
				t.Fatal("rejected write changed existing evidence")
			}
		})
	}
}

func TestBatchCacheAdvancesFromUnknownToSharedCompleteEvidence(t *testing.T) {
	r := receiptFixture(t)
	r.Attempts[0].State = "unknown"
	store := Store{Dir: t.TempDir()}
	unlock, err := store.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = store.SaveBatch(r); err != nil {
		t.Fatal(err)
	}
	r.Attempts[0].State = "complete"
	if err = store.SaveBatch(r); err != nil {
		t.Fatal(err)
	}
	parent := r.Attempts[0].AttemptID
	id, _ := AttemptID(r.RequestID, parent)
	r.Attempts = append(r.Attempts, AttemptReceipt{AttemptID: id, ParentID: parent, ClientToken: attemptToken(r.PlanSHA256, id, 1), RequestedCount: 1, CreatedAt: r.Plan.CreatedAt, State: "prepared", InstanceIDs: []string{}, Errors: []ResourceError{}})
	if err = store.SaveBatch(r); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadBatch(r.RequestID)
	if err != nil || len(loaded.Attempts) != 2 || len(loaded.FulfilledIDs()) != 1 {
		t.Fatal("lost lineage", err)
	}
}
