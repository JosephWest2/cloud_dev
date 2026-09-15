package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
)

// These fixtures were captured from a0988bf before expiry fields existed. Never
// regenerate them from the current structs: old permanent ledger objects and
// their dispatch tokens must continue to verify after a schema migration.
func TestExpiryMigrationPreservesHistoricalBytesAndDigest(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile("testdata/expiry-legacy/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSuffix(b, []byte("\n"))
	}
	var plan LaunchPlan
	var batch BatchReceipt
	var legacy Receipt
	var prepared PreparedAttempt
	var claim DispatchClaim
	var response AttemptResponse
	for name, value := range map[string]any{"plan-v1": &plan, "batch-v2": &batch, "receipt-v1": &legacy, "prepared-v1": &prepared, "claim-v1": &claim, "response-v1": &response} {
		b := read(name + ".json")
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(value); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, encoded) {
			t.Fatalf("%s historical serialization changed", name)
		}
	}
	want := string(read("plan-v1.sha256"))
	sum := sha256.Sum256(read("plan-v1.json"))
	if hex.EncodeToString(sum[:]) != want || plan.Digest() != want || batch.PlanSHA256 != want {
		t.Fatal("historical plan digest changed")
	}
	if err := batch.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := legacy.validate(legacy.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBatchReceipt(read("batch-v2.json")); err != nil {
		t.Fatal(err)
	}
	base := batch
	base.Attempts = nil
	if !validPrepared(base, prepared) || expectedClaim(prepared) != claim || !validResponse(base, prepared, response) {
		t.Fatal("permanent launch record evidence no longer verifies")
	}
	// The new allocation policy rejects even a historically prepared, otherwise
	// valid request. Merely decoding/validating its evidence remains supported.
	err := expiry.CheckAllocation(plan.SchemaVersion, expiry.Window{CreatedAt: plan.CreatedAt}, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), false)
	if err == nil || err.Error() != string(expiry.LegacyRequest) {
		t.Fatalf("legacy request dispatch policy: %v", err)
	}
	// Mutating arbitrary field order via a map is deliberately not a migration.
	if strings.Contains(string(read("plan-v1.json")), "expires_at") {
		t.Fatal("historical fixture was retrofitted")
	}
}
