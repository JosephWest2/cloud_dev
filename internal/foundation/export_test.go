package foundation

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/cleanuplambda"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

// make infra-check supplies actual OpenTofu mock-provider output. Ordinary Go
// tests need no infrastructure executable or state. This catches exporter/schema
// incompatibilities and verifies Go hashes against OpenTofu's real jsonencode.
func TestOpenTofuExport(t *testing.T) {
	path := os.Getenv("DEVBOX_TEST_TOFU_OUTPUT")
	if path == "" {
		t.Skip("run make infra-check for the OpenTofu export contract")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	found := false
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Message string `json:"@message"`
			Summary struct {
				Status string `json:"status"`
			} `json:"test_summary"`
			Diagnostic struct {
				Summary string `json:"summary"`
				Detail  string `json:"detail"`
			} `json:"diagnostic"`
			State struct {
				Values struct {
					Outputs map[string]struct {
						Value     json.RawMessage `json:"value"`
						Sensitive bool            `json:"sensitive"`
					} `json:"outputs"`
					Root struct {
						Resources []struct {
							Address string                     `json:"address"`
							Values  map[string]json.RawMessage `json:"values"`
						} `json:"resources"`
					} `json:"root_module"`
				} `json:"values"`
			} `json:"test_state"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "test_run" || event.Type == "test_summary" {
			t.Log(event.Message)
		}
		if event.Type == "test_summary" && event.Summary.Status != "pass" {
			t.Error("OpenTofu test suite did not pass")
		}
		if event.Type == "diagnostic" {
			t.Log(event.Diagnostic.Summary, event.Diagnostic.Detail)
		}
		if event.Type != "test_state" {
			continue
		}
		out, ok := event.State.Values.Outputs["deployment_manifest"]
		if !ok {
			continue
		}
		if out.Sensitive {
			t.Fatal("manifest must be non-secret")
		}
		found = true
		c, err := config.Load(testutil.Setup(t), config.Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		p, err := config.LoadProfile("")
		if err != nil {
			t.Fatal(err)
		}
		manifestPath := testutil.Write(t, filepath.Join(t.TempDir(), "deployment.json"), string(out.Value))
		m, err := config.LoadManifest(manifestPath, c, p)
		if err != nil {
			t.Fatal("OpenTofu exported an invalid CLI manifest:", err)
		}
		if m.SchemaVersion != 6 {
			t.Fatal("foundation must export manifest version 6")
		}
		resources := map[string]map[string]json.RawMessage{}
		for _, r := range event.State.Values.Root.Resources {
			resources[r.Address] = r.Values
		}
		cleanup, err := config.DecodeCleanup(m.Cleanup, m)
		if err != nil {
			t.Fatal("OpenTofu cleanup descriptor:", err)
		}
		evidence, err := config.DecodeEvidence(cleanup.Evidence, m, cleanup)
		if err != nil {
			t.Fatal("OpenTofu evidence descriptor:", err)
		}
		verifyEvidenceExport(t, m, cleanup, evidence, resources)
		var zipDigest string
		if err := json.Unmarshal(resources["aws_lambda_function.cleanup"]["source_code_hash"], &zipDigest); err != nil {
			t.Fatal(err)
		}
		bytes, err := base64.StdEncoding.DecodeString(zipDigest)
		if err != nil || fmt.Sprintf("%x", bytes) != cleanup.Function.CodeSHA256 {
			t.Fatal("Lambda base64/manifest hex code digest mismatch")
		}
		var environments []struct {
			Variables map[string]string `json:"variables"`
		}
		if err := json.Unmarshal(resources["aws_lambda_function.cleanup"]["environment"], &environments); err != nil || len(environments) != 1 {
			t.Fatal("missing cleanup environment")
		}
		environment, _ := json.Marshal(environments[0].Variables)
		if digest(environment) != cleanup.Function.EnvironmentSHA256 {
			t.Fatal("cleanup environment digest mismatch")
		}
		var targets []struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(resources["aws_scheduler_schedule.cleanup"]["target"], &targets); err != nil || len(targets) != 1 {
			t.Fatal("missing cleanup schedule input")
		}
		inputHash, err := jsonDigest(targets[0].Input)
		if err != nil || inputHash != cleanup.Schedule.InputSHA256 {
			t.Fatal("cleanup schedule input digest mismatch")
		}
		verifySchedulerDelivery(t, targets[0].Input, cleanup)
		verifyPlacementExport(t, m, resources)
		expected := map[string]map[string]string{
			"aws_iam_role.evidence":                 {"assume_role_policy": evidence.PipeRole.TrustSHA256},
			"aws_iam_role.cleanup_health":           {"assume_role_policy": evidence.HealthRole.TrustSHA256},
			"aws_iam_role_policy.evidence":          {"policy": evidence.PipeRole.PolicySHA256},
			"aws_iam_role_policy.cleanup_health":    {"policy": evidence.HealthRole.PolicySHA256},
			"aws_iam_role.cleanup":                  {"assume_role_policy": cleanup.ExecutionRole.TrustSHA256},
			"aws_iam_role.cleanup_scheduler":        {"assume_role_policy": cleanup.SchedulerRole.TrustSHA256},
			"aws_iam_role_policy.cleanup":           {"policy": cleanup.ExecutionRole.PolicySHA256},
			"aws_iam_role_policy.cleanup_scheduler": {"policy": cleanup.SchedulerRole.PolicySHA256},
			"aws_iam_role.instance":                 {"assume_role_policy": m.Roles["instance"].TrustSHA256},
			"aws_iam_role.operator":                 {"assume_role_policy": m.Roles["operator"].TrustSHA256},
			"aws_iam_role_policy.instance":          {"policy": m.Roles["instance"].PolicySHA256},
			"aws_iam_role_policy.operator":          {"policy": m.Roles["operator"].PolicySHA256},
			"aws_ssm_document.readiness":            {"content": m.Readiness.ContentSHA256},
			"aws_ssm_document.execution":            {"content": m.Execution.ContentSHA256},
			"aws_s3_bucket_policy.results":          {"policy": m.Results.PolicySHA256},
			"aws_launch_template.agent":             {"user_data": m.BootstrapSHA256},
		}
		for _, r := range event.State.Values.Root.Resources {
			for field, want := range expected[r.Address] {
				var document string
				if err := json.Unmarshal(r.Values[field], &document); err != nil {
					t.Fatal(err)
				}
				var got string
				if field == "user_data" {
					b, err := base64.StdEncoding.DecodeString(document)
					if err != nil {
						t.Fatal(err)
					}
					got = digest(b)
				} else {
					got, err = jsonDigest(document)
					if err != nil {
						t.Fatal(err)
					}
				}
				if got != want {
					t.Fatalf("%s.%s digest differs between Go and OpenTofu", r.Address, field)
				}
			}
			delete(expected, r.Address)
		}
		if len(expected) != 0 {
			t.Fatal("missing export resources", expected)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no mock-applied manifest in OpenTofu test output")
	}
}

// This crosses the provisioned string boundary: JSON equivalence alone cannot
// prove that Scheduler finds its literal context keywords before delivery.
func verifySchedulerDelivery(t *testing.T, input string, cleanup config.Cleanup) {
	t.Helper()
	values := []string{
		"<aws.scheduler.schedule-arn>", cleanup.Schedule.ARN,
		"<aws.scheduler.scheduled-time>", "2026-09-14T12:00:00Z",
		"<aws.scheduler.execution-id>", "d32c5kddcf5bb8c3",
		"<aws.scheduler.attempt-number>", "1",
	}
	for i := 0; i < len(values); i += 2 {
		if strings.Count(input, values[i]) != 1 {
			t.Fatalf("rendered Scheduler input must contain exactly one literal %s", values[i])
		}
	}
	substitute := strings.NewReplacer(values...).Replace
	delivered, err := cleanuplambda.Decode([]byte(substitute(input)))
	if err != nil || delivered.SchemaVersion != 1 || delivered.ScheduleARN != values[1] || delivered.ScheduledTime != values[3] || delivered.ExecutionID != values[5] || delivered.AttemptNumber != values[7] {
		t.Fatalf("rendered Scheduler input failed real adapter decoding after substitution: %+v %v", delivered, err)
	}
	// Recreate the original broken transport as a negative control. Its canonical
	// digest still matches, demonstrating why the digest bridge alone missed it.
	var object map[string]any
	if err := json.Unmarshal([]byte(input), &object); err != nil {
		t.Fatal(err)
	}
	escaped, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if digest(escaped) != cleanup.Schedule.InputSHA256 || digest([]byte(input)) == cleanup.Schedule.InputSHA256 {
		t.Fatal("manifest must retain the canonical digest, separate from literal transport bytes")
	}
	if _, err := cleanuplambda.Decode([]byte(substitute(string(escaped)))); err == nil {
		t.Fatal("escaped-keyword negative control unexpectedly delivered valid correlation")
	}
	restored := strings.NewReplacer(`\u003c`, "<", `\u003e`, ">").Replace(string(escaped))
	positive, err := cleanuplambda.Decode([]byte(substitute(restored)))
	if err != nil || positive != delivered {
		t.Fatalf("restored-keyword positive control: %+v %v", positive, err)
	}
}

func verifyPlacementExport(t *testing.T, m config.Manifest, resources map[string]map[string]json.RawMessage) {
	t.Helper()
	var template struct {
		ID                string `json:"id"`
		Version           int    `json:"latest_version"`
		ImageID           string `json:"image_id"`
		NetworkInterfaces []struct {
			DeviceIndex int      `json:"device_index"`
			SubnetID    string   `json:"subnet_id"`
			Groups      []string `json:"security_groups"`
			Public      string   `json:"associate_public_ip_address"`
			Delete      string   `json:"delete_on_termination"`
		} `json:"network_interfaces"`
		Blocks []struct {
			Device string `json:"device_name"`
			EBS    []struct {
				Size      int    `json:"volume_size"`
				Type      string `json:"volume_type"`
				Encrypted string `json:"encrypted"`
				Delete    string `json:"delete_on_termination"`
			} `json:"ebs"`
		} `json:"block_device_mappings"`
	}
	decode := func(address string, target any) {
		t.Helper()
		raw, exists := resources[address]
		if !exists {
			t.Fatal("missing placement export resource", address)
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, target); err != nil {
			t.Fatal(address, err)
		}
	}
	decode("aws_launch_template.agent", &template)
	img := m.Images["agent"]
	if template.ID != img.LaunchTemplateID || strconv.Itoa(template.Version) != img.LaunchTemplateVersion || template.ImageID != img.AMIID || len(template.NetworkInterfaces) != 1 || len(template.Blocks) != 1 {
		t.Fatal("template export identity/mappings differ")
	}
	ni := template.NetworkInterfaces[0]
	if ni.DeviceIndex != 0 || ni.SubnetID != "" || len(ni.Groups) != 1 || ni.Groups[0] != m.SecurityGroupID || ni.Public != "true" || ni.Delete != "true" {
		t.Fatal("exported template cannot safely accept subnet overrides")
	}
	block := template.Blocks[0]
	if block.Device != img.RootDeviceName || len(block.EBS) != 1 {
		t.Fatal("template root mapping differs")
	}
	e := block.EBS[0]
	if img.RootDisk == nil || e.Size != img.RootDisk.SizeGB || e.Type != img.RootDisk.Type || e.Encrypted != strconv.FormatBool(img.RootDisk.Encrypted) || e.Delete != strconv.FormatBool(img.RootDisk.DeleteOnTermination) {
		t.Fatal("template root defaults differ from export")
	}
	var image struct {
		ID     string `json:"id"`
		Root   string `json:"root_device_name"`
		Blocks []struct {
			Device string `json:"device_name"`
			EBS    struct {
				Size string `json:"volume_size"`
			} `json:"ebs"`
		} `json:"block_device_mappings"`
	}
	decode("data.aws_ami.ubuntu", &image)
	count := 0
	for _, block := range image.Blocks {
		if block.Device == img.RootDeviceName {
			count++
			if block.EBS.Size != strconv.Itoa(img.MinimumRootDiskGB) {
				t.Fatal("AMI snapshot minimum differs from export")
			}
		}
	}
	if image.ID != img.AMIID || image.Root != img.RootDeviceName || count != 1 {
		t.Fatal("AMI root export differs")
	}
	for _, subnet := range m.Subnets {
		address := "aws_subnet.devbox"
		association := "aws_route_table_association.devbox"
		if subnet.AvailabilityZone != "us-east-2a" {
			address = "aws_subnet.additional[" + strconv.Quote(subnet.AvailabilityZone) + "]"
			association = "aws_route_table_association.additional[" + strconv.Quote(subnet.AvailabilityZone) + "]"
		}
		var actual struct {
			ID  string `json:"id"`
			AZ  string `json:"availability_zone"`
			VPC string `json:"vpc_id"`
		}
		decode(address, &actual)
		if actual.ID != subnet.ID || actual.AZ != subnet.AvailabilityZone || actual.VPC != m.VPCID {
			t.Fatal("subnet export differs", address)
		}
		var route struct {
			Subnet string `json:"subnet_id"`
			Table  string `json:"route_table_id"`
		}
		decode(association, &route)
		if route.Subnet != subnet.ID || route.Table != m.RouteTableID {
			t.Fatal("selected subnet lacks its exported route association")
		}
		var offerings struct {
			Types []string `json:"instance_types"`
		}
		decode("data.aws_ec2_instance_type_offerings.selected["+strconv.Quote(subnet.AvailabilityZone)+"]", &offerings)
		for _, pool := range m.CompatiblePools {
			if slices.Contains(pool.SubnetIDs, subnet.ID) != slices.Contains(offerings.Types, pool.InstanceType) {
				t.Fatal("exported pool is not the exact offering intersection", pool.InstanceType, subnet.AvailabilityZone)
			}
		}
	}
	var bucket struct {
		Policy string `json:"policy"`
	}
	decode("aws_s3_bucket_policy.results", &bucket)
	if m.LaunchLedger == nil || !immutableLaunchPolicy(bucket.Policy, *m.LaunchLedger) {
		t.Fatal("exported ledger lacks enforced conditional creation")
	}
	var lifecycle struct {
		Rule []struct {
			Filter []struct {
				Prefix string `json:"prefix"`
			} `json:"filter"`
		} `json:"rule"`
	}
	decode("aws_s3_bucket_lifecycle_configuration.results", &lifecycle)
	if len(lifecycle.Rule) == 0 {
		t.Fatal("missing result retention")
	}
	for _, rule := range lifecycle.Rule {
		if len(rule.Filter) != 1 || rule.Filter[0].Prefix != m.Results.Prefix || strings.HasPrefix(m.LaunchLedger.Prefix, rule.Filter[0].Prefix) {
			t.Fatal("ledger covered by automatic expiration")
		}
	}
}
