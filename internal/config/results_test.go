package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestResultRecoveryDescriptorWithoutLaunchOrSSH(t *testing.T) {
	c, err := Load(testutil.Setup(t), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(testutil.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	for key := range manifest {
		switch key {
		case "schema_version", "account", "region", "deployment", "owner", "results":
		default:
			delete(manifest, key)
		}
	}
	b, _ := json.Marshal(manifest)
	testutil.Write(t, c.Manifest, string(b))
	c.ProfileFile, c.SSHIdentityFile = "/missing/profile", "/missing/key"
	got, err := LoadResultManifest(c.Manifest, c)
	if err != nil || got.Prefix != ResultsPrefix(c) {
		t.Fatalf("independent recovery descriptor: %+v %v", got, err)
	}
	if _, err := LoadManifest(c.Manifest, c, Profile{Image: "agent"}); err == nil {
		t.Fatal("storage-only descriptor authorized a new launch")
	}
}

func TestExecutionAndStorageManifestBindings(t *testing.T) {
	c, err := Load(testutil.Setup(t), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := LoadProfile("")
	for _, change := range []struct{ old, new string }{
		{`"schema_version":4`, `"schema_version":3`},
		{`"step":"execute"`, `"step":"bootstrapStatus"`},
		{`"name":"devbox-test-execution"`, `"name":"devbox-test-readiness"`},
		{`"minimum_agent_version":"3.3.2746.0"`, `"minimum_agent_version":"3.3.40.0"`},
		{`"retention_days":30`, `"retention_days":1`},
		{`"retention_days":30`, `"retention_days":366`},
		{`"expected_bucket_owner":"123456789012"`, `"expected_bucket_owner":"000000000000"`},
		{`"bucket":"devbox-results-test"`, `"bucket":"https://other-bucket"`},
		{`results/v1/123456789012/us-east-2/test/test-owner/`, `results/v1/123456789012/us-east-2/test/other-owner/`},
		{`results/v1/123456789012/us-east-2/test/test-owner/`, `results/v1/123456789012/us-east-2/test/test-owner/../`},
	} {
		t.Run(change.new, func(t *testing.T) {
			data := strings.Replace(testutil.Manifest, change.old, change.new, 1)
			if data == testutil.Manifest {
				t.Fatal("fixture did not change")
			}
			testutil.Write(t, c.Manifest, data)
			if _, err := LoadManifest(c.Manifest, c, p); err == nil {
				t.Fatal("invalid execution/storage accepted")
			}
		})
	}
	if err := os.Remove(c.Manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadResultManifest(c.Manifest, c); err == nil {
		t.Fatal("missing result descriptor accepted")
	}
}
