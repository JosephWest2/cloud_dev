package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func batchFixture(t *testing.T) (config.Config, config.Manifest, config.Profile, LaunchSelection) {
	t.Helper()
	c, err := config.Load(testutil.Setup(t), config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	m, err := config.LoadManifest(c.Manifest, c, p)
	if err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion = 6
	m.SubnetIDs = []string{"subnet-12345678", "subnet-87654321"}
	m.Subnets = []config.Subnet{{ID: m.SubnetIDs[0], AvailabilityZone: "us-east-2a"}, {ID: m.SubnetIDs[1], AvailabilityZone: "us-east-2b"}}
	p.InstanceTypes = []string{"c7i.2xlarge", "c6i.2xlarge"}
	m.CompatiblePools = []config.CompatiblePool{
		{InstanceType: p.InstanceTypes[0], Architecture: "x86_64", SubnetIDs: m.SubnetIDs},
		{InstanceType: p.InstanceTypes[1], Architecture: "x86_64", SubnetIDs: m.SubnetIDs[1:]},
	}
	i := m.Images["agent"]
	i.RootDisk = &config.RootDisk{SizeGB: 100, Type: "gp3", Encrypted: true, DeleteOnTermination: true}
	i.MinimumRootDiskGB = 8
	m.Images["agent"] = i
	m.LaunchLedger = &config.LaunchLedger{SchemaVersion: 1, Bucket: m.Results.Bucket, ExpectedBucketOwner: m.Account, Region: m.Region, Prefix: config.LaunchLedgerPrefix(c), PolicySHA256: m.Results.PolicySHA256}
	s, err := ResolveLaunchSelection([]string{"agent"}, LaunchFlags{Count: "2", Group: "smoke-batch"}, c.MaxCount)
	if err != nil {
		t.Fatal(err)
	}
	return c, m, p, s
}

func TestResolvedLaunchPlanUsesOnlyApprovedCombinations(t *testing.T) {
	c, m, p, s := batchFixture(t)
	plan, err := BuildLaunchPlan(c, m, p, s, strings.Repeat("a", 32), "2026-09-14T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Choices) != 3 || plan.Market != "spot" || plan.RequestedCount != 2 || plan.BaseName != "smoke-batch" || plan.RootDisk.Type != "gp3" {
		t.Fatalf("bad plan: %+v", plan)
	}
	for _, choice := range plan.Choices {
		if choice.InstanceType == "c6i.2xlarge" && choice.SubnetID == "subnet-12345678" {
			t.Fatal("invented unsupported pool")
		}
	}
	digest := plan.Digest()
	m.Images["agent"].RootDisk.SizeGB = 200
	m.CompatiblePools[0].SubnetIDs[0] = "subnet-aaaaaaaa"
	if plan.Digest() != digest {
		t.Fatal("resolved plan aliases mutable configuration")
	}
	preview := plan.Preview()
	for _, field := range []string{"profile=agent", "region=us-east-2", "count=2", "market=spot", "group=smoke-batch", "c7i.2xlarge", "subnet-12345678", "us-east-2a"} {
		if !strings.Contains(preview, field) {
			t.Fatalf("preview missing %s: %s", field, preview)
		}
	}
}

func TestLaunchPlanRejectsBeforeAllocation(t *testing.T) {
	for _, name := range []string{"zero", "excessive", "invalid-limit", "scope", "on-demand-profile", "arm", "legacy-manifest", "disk", "unknown-type", "unknown-subnet", "resume"} {
		t.Run(name, func(t *testing.T) {
			c, m, p, s := batchFixture(t)
			switch name {
			case "zero":
				s.Count = 0
			case "excessive":
				s.Count = c.MaxCount + 1
			case "invalid-limit":
				c.MaxCount = 101
			case "scope":
				c.Owner = "different"
			case "on-demand-profile":
				p.Market = "on-demand"
			case "arm":
				p.Architecture = "arm64"
			case "legacy-manifest":
				m.SchemaVersion = 4
			case "disk":
				p.DiskGB = 7
			case "unknown-type":
				p.InstanceTypes = []string{"c8g.2xlarge"}
			case "unknown-subnet":
				p.SubnetIDs = []string{"subnet-aaaaaaaa"}
			case "resume":
				s.Resume = strings.Repeat("b", 32)
			}
			if _, err := BuildLaunchPlan(c, m, p, s, strings.Repeat("a", 32), "2026-09-14T00:00:00Z"); err == nil {
				t.Fatal("invalid new request accepted")
			}
		})
	}
	c, m, p, s := batchFixture(t)
	p.Market = "on-demand"
	s.OnDemand = true
	plan, err := BuildLaunchPlan(c, m, p, s, strings.Repeat("a", 32), "2026-09-14T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Market != "on-demand" || len(plan.Choices) != 2 {
		t.Fatalf("bad explicit market plan: %+v", plan)
	}
	for _, choice := range plan.Choices {
		if choice.InstanceType != p.InstanceTypes[0] {
			t.Fatal("On-Demand must preserve first profile type")
		}
	}
}

func TestWorkerNamesAndCreationTagsSurviveOrderAndPartialResults(t *testing.T) {
	base := strings.Repeat("b", 63)
	ids := []string{"i-0123456789abcdef0", "i-0123456789abcdef1", "i-12345678"}
	names := map[string]string{}
	for _, id := range ids {
		name, err := WorkerName(base, id)
		if err != nil || len(name) != 63 || !ValidName(name) || !strings.HasSuffix(name, "-"+id) {
			t.Fatalf("bad name %s: %v", name, err)
		}
		names[id] = name
	}
	// A later partial response or retry cannot renumber any existing worker.
	for _, id := range []string{ids[2], ids[0], ids[1]} {
		name, _ := WorkerName(base, id)
		if names[id] != name {
			t.Fatal("name depends on response order")
		}
	}
	c, m, p, s := batchFixture(t)
	plan, err := BuildLaunchPlan(c, m, p, s, strings.Repeat("a", 32), "2026-09-14T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	aid, _ := AttemptID(plan.RequestID, "")
	tags, err := plan.AttemptTags(aid)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ManagedBy", "Deployment", "Owner", "Profile", "RequestId", "BatchId", "AttemptId", "Group", "BaseName", "NamingVersion", "CreatedAt", "Name"} {
		if tags[key] == "" {
			t.Fatalf("missing creation tag %s", key)
		}
	}
	tags["Owner"] = "changed"
	if plan.CreationTags["Owner"] != c.Owner {
		t.Fatal("attempt tags alias plan")
	}
}

func receiptFixture(t *testing.T) BatchReceipt {
	c, m, p, s := batchFixture(t)
	plan, err := BuildLaunchPlan(c, m, p, s, strings.Repeat("a", 32), "2026-09-14T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	aid, _ := AttemptID(plan.RequestID, "")
	r := BatchReceipt{SchemaVersion: 3, RequestID: plan.RequestID, Plan: plan, PlanSHA256: plan.Digest()}
	r.Attempts = []AttemptReceipt{{AttemptID: aid, ClientToken: attemptToken(r.PlanSHA256, aid, 2), RequestedCount: 2, CreatedAt: plan.CreatedAt, State: "complete", FleetID: "fleet-01234567-89ab-cdef-0123-456789abcdef", InstanceIDs: []string{"i-12345678"}, Errors: []ResourceError{{Code: "capacity_unavailable", InstanceType: "c6i.2xlarge"}}}}
	return r
}

func TestMissingCapacityPreservesHistoricFulfillmentAndLineage(t *testing.T) {
	r := receiptFixture(t)
	first := r.Attempts[0].AttemptID
	missing, err := r.MissingCapacity(first)
	if err != nil || missing != 1 {
		t.Fatalf("partial remainder: %d %v", missing, err)
	}
	second, _ := AttemptID(r.RequestID, first)
	r.Attempts = append(r.Attempts, AttemptReceipt{AttemptID: second, ParentID: first, ClientToken: attemptToken(r.PlanSHA256, second, 1), RequestedCount: 1, CreatedAt: r.Plan.CreatedAt, State: "complete", FleetID: "fleet-11234567-89ab-cdef-0123-456789abcdef", InstanceIDs: []string{"i-87654321"}})
	// No inventory argument exists: terminating the first worker cannot erase its
	// original fulfillment or grant a replacement.
	missing, err = r.MissingCapacity(second)
	if err != nil || missing != 0 || !reflect.DeepEqual(r.FulfilledIDs(), []string{"i-12345678", "i-87654321"}) {
		t.Fatalf("historic fulfillment lost: %d %v", missing, err)
	}
	if _, err = r.MissingCapacity(first); err == nil {
		t.Fatal("repeated action ignored existing successor")
	}
	r.Attempts[1].RequestedCount = 2
	if r.Validate() == nil {
		t.Fatal("retry requested the full original batch")
	}
}

func TestUnknownAndCorruptReceiptsCannotProveMissingCapacity(t *testing.T) {
	for _, state := range []string{"prepared", "dispatched", "unknown"} {
		r := receiptFixture(t)
		r.Attempts[0].State = state
		if state == "prepared" {
			r.Attempts[0].InstanceIDs = nil
			r.Attempts[0].FleetID = ""
		}
		if _, err := r.MissingCapacity(r.Attempts[0].AttemptID); err == nil {
			t.Fatalf("%s authorized a retry", state)
		}
	}
	r := receiptFixture(t)
	encoded, _ := json.Marshal(r)
	if _, err := decodeBatchReceipt(encoded); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{append(append([]byte{}, encoded...), []byte("{}")...), []byte(strings.Replace(string(encoded), `"schema_version":2`, `"schema_version":99`, 1)), []byte(strings.Replace(string(encoded), `"requested_count":2`, `"requested_count":3`, 1)), []byte(`{"schema_version":2,"secret":"SECRET"}`)} {
		if _, err := decodeBatchReceipt(data); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe receipt accepted: %v", err)
		}
	}
	r.Attempts[0].InstanceIDs = append(r.Attempts[0].InstanceIDs, "i-12345678")
	if r.Validate() == nil {
		t.Fatal("duplicate identity counted twice")
	}
}

func TestBatchReceiptRejectsInvalidAndReusedTerminalFleetIDs(t *testing.T) {
	r := receiptFixture(t)
	r.Attempts[0].FleetID = "invalid-fleet"
	if r.Validate() == nil {
		t.Fatal("invalid Fleet identity accepted")
	}
	r = receiptFixture(t)
	parent := r.Attempts[0].AttemptID
	id, _ := AttemptID(r.RequestID, parent)
	r.Attempts = append(r.Attempts, AttemptReceipt{AttemptID: id, ParentID: parent,
		ClientToken: attemptToken(r.PlanSHA256, id, 1), RequestedCount: 1, CreatedAt: r.Plan.CreatedAt,
		State: "complete", FleetID: r.Attempts[0].FleetID, InstanceIDs: []string{"i-87654321"}})
	if r.Validate() == nil {
		t.Fatal("two terminal attempts reused one Fleet identity")
	}
	r.Attempts[1].State = "unknown"
	if r.Validate() != nil {
		t.Fatal("contradictory Fleet evidence could not be retained as unknown")
	}
	if _, err := r.MissingCapacity(id); err == nil {
		t.Fatal("contradictory Fleet evidence authorized a successor")
	}
}

func TestCompleteReceiptRequiresExplicitFulfillmentArray(t *testing.T) {
	r := receiptFixture(t)
	data, _ := json.Marshal(r)
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	attempt := wire["attempts"].([]any)[0].(map[string]any)
	for _, null := range []bool{false, true} {
		delete(attempt, "instance_ids")
		if null {
			attempt["instance_ids"] = nil
		}
		invalid, _ := json.Marshal(wire)
		if _, err := decodeBatchReceipt(invalid); err == nil {
			t.Fatal("missing/null fulfillment field became proven zero")
		}
	}
	// A complete explicit zero response differs from an omitted response field.
	attempt["instance_ids"] = []string{}
	zero, _ := json.Marshal(wire)
	decoded, err := decodeBatchReceipt(zero)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := decoded.MissingCapacity(decoded.Attempts[0].AttemptID)
	if err != nil || missing != 2 {
		t.Fatalf("explicit no-capacity response: %d %v", missing, err)
	}
}

func TestLegacyLaunchCannotBypassV5Integration(t *testing.T) {
	c, m, _, _ := batchFixture(t)
	m.SchemaVersion = 5
	// The bundled profile needs each type in the manifest even though the
	// legacy allocator would have selected only its first type.
	p, err := config.LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	m.CompatiblePools = nil
	for _, typ := range p.InstanceTypes {
		m.CompatiblePools = append(m.CompatiblePools, config.CompatiblePool{InstanceType: typ, Architecture: "x86_64", SubnetIDs: m.SubnetIDs})
	}
	data, _ := json.Marshal(m)
	testutil.Write(t, c.Manifest, string(data))
	path := testutil.Write(t, strings.TrimSuffix(c.Manifest, "deployment.json")+"config.toml", testutil.Config)
	result := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", UpOptions: UpOptions{Name: "legacy", OnDemand: true}}, Dependencies{New: func(context.Context, config.Config) (*Service, error) {
		t.Fatal("version 5 reached legacy allocator")
		return nil, nil
	}}, io.Discard)
	if result.ExitCode != 2 || result.Code != "manifest_upgrade_required" {
		t.Fatalf("legacy path bypassed integration: %+v", result)
	}
}
