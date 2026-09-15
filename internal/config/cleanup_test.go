package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupScopeOnly(t *testing.T) {
	t.Setenv("AWS_PROFILE", "environment")
	path := filepath.Join(t.TempDir(), "config.toml")
	text := `schema_version=1
expected_account="123456789012"
region="us-east-2"
deployment="dev"
owner="joe"
aws_profile="configured"
manifest="missing.json"
profile_file="missing.toml"
ssh_identity_file="missing"
max_count=-1
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCleanup(path, Overrides{AWSProfile: "explicit", Region: "us-west-2"})
	if err != nil || c.AWSProfile != "explicit" || c.Region != "us-west-2" || c.ExpectedAccount != "123456789012" || c.Deployment != "dev" || c.Owner != "joe" {
		t.Fatalf("%+v %v", c, err)
	}
	if c.Manifest != "" || c.ProfileFile != "" || c.SSHIdentityFile != "" {
		t.Fatalf("launch paths leaked: %+v", c)
	}
	c, err = LoadCleanup(path, Overrides{})
	if err != nil || c.AWSProfile != "environment" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("AWS_PROFILE", "")
	c, err = LoadCleanup(path, Overrides{})
	if err != nil || c.AWSProfile != "configured" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, extra := range []string{"credentials=\"secret\"\n", "unexpected=3\n"} {
		if err := os.WriteFile(path, []byte(text+extra), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCleanup(path, Overrides{}); err == nil {
			t.Fatal("accepted unknown field")
		}
	}
}
