package foundation

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
		expected := map[string]map[string]string{
			"aws_iam_role.instance":        {"assume_role_policy": m.Roles["instance"].TrustSHA256},
			"aws_iam_role.operator":        {"assume_role_policy": m.Roles["operator"].TrustSHA256},
			"aws_iam_role_policy.instance": {"policy": m.Roles["instance"].PolicySHA256},
			"aws_iam_role_policy.operator": {"policy": m.Roles["operator"].PolicySHA256},
			"aws_ssm_document.readiness":   {"content": m.Readiness.ContentSHA256},
			"aws_ssm_document.execution":   {"content": m.Execution.ContentSHA256},
			"aws_s3_bucket_policy.results": {"policy": m.Results.PolicySHA256},
			"aws_launch_template.agent":    {"user_data": m.BootstrapSHA256},
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
