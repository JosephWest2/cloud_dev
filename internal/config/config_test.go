package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestConfigPrecedenceAndPaths(t *testing.T) {
	t.Setenv("AWS_PROFILE", "environment")
	t.Setenv("AWS_REGION", "us-west-2")
	path := testutil.Setup(t)
	c, err := Load(path, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.AWSProfile != "environment" || c.Region != "us-east-2" || c.Manifest != filepath.Join(filepath.Dir(path), "deployment.json") {
		t.Fatalf("unexpected precedence/path: %+v", c)
	}
	c, err = Load(path, Overrides{AWSProfile: "flag", Region: "us-west-1"})
	if err != nil || c.AWSProfile != "flag" || c.Region != "us-west-1" {
		t.Fatalf("flag precedence: %+v %v", c, err)
	}
	t.Setenv("AWS_PROFILE", "")
	c, err = Load(path, Overrides{})
	if err != nil || c.AWSProfile != "test" {
		t.Fatalf("TOML profile: %+v %v", c, err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(path))
	got, err := DefaultPath()
	if err != nil || got != filepath.Join(filepath.Dir(path), "devbox", "config.toml") {
		t.Fatalf("XDG path: %s %v", got, err)
	}
}

func TestInvalidConfigIsRedacted(t *testing.T) {
	for _, data := range []string{
		`schema_version = "SECRET`,
		strings.Replace(testutil.Config, "schema_version = 1", "schema_version = 2", 1),
		strings.Replace(testutil.Config, "expected_account", "SECRET", 1),
		strings.Replace(testutil.Config, "123456789012", "SECRET", 1),
		strings.Replace(testutil.Config, "test-owner", "SECRET/owner", 1),
		testutil.Config + "aws_secret_access_key = 'SECRET'\n",
	} {
		path := testutil.Write(t, filepath.Join(t.TempDir(), "config.toml"), data)
		_, err := Load(path, Overrides{})
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("expected safe rejection, got %v", err)
		}
	}
}

func TestProfileContract(t *testing.T) {
	p, err := LoadProfile("")
	if err != nil || p.Market != "spot" {
		t.Fatalf("embedded Spot default: %+v %v", p, err)
	}
	base := "schema_version=1\nname='agent'\nmarket='spot'\ninstance_types=['c7i.2xlarge']\nimage='agent'\ndisk_gb=100\n"
	for _, instanceType := range []string{"c7i.2xlarge", "c7i.metal-24xl", "u-12tb1.112xlarge"} {
		data := strings.Replace(base, "c7i.2xlarge", instanceType, 1)
		p, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), data))
		if err != nil || len(p.InstanceTypes) != 1 || p.InstanceTypes[0] != instanceType {
			t.Fatalf("valid EC2 instance type %q rejected: %v", instanceType, err)
		}
	}
	for _, data := range []string{
		strings.Replace(base, "schema_version=1", "schema_version=2", 1),
		strings.Replace(base, "market='spot'", "market='automatic'", 1),
		strings.Replace(base, "['c7i.2xlarge']", "[]", 1),
		strings.Replace(base, "c7i.2xlarge", "c7i/metal-24xl", 1),
		strings.Replace(base, "c7i.2xlarge", "c7i.", 1),
		strings.Replace(base, "c7i.2xlarge", "c7i.metal.24xl", 1),
		strings.Replace(base, "disk_gb=100", "disk_gb=-1", 1),
		base + "secret='SECRET'\n",
	} {
		_, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), data))
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("expected safe profile rejection: %v", err)
		}
	}
}

func TestManifestScopeAndPinnedResources(t *testing.T) {
	c, err := Load(testutil.Setup(t), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(c.Manifest, c, p); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		strings.Replace(testutil.Manifest, `"schema_version":3`, `"schema_version":1`, 1),
		strings.Replace(testutil.Manifest, "123456789012", "000000000000", 1),
		strings.Replace(testutil.Manifest, "us-east-2", "us-west-2", 1),
		strings.Replace(testutil.Manifest, `"deployment":"test"`, `"deployment":"different"`, 1),
		strings.Replace(testutil.Manifest, "test-owner", "other-owner", 1),
		strings.Replace(testutil.Manifest, `"launch_template_version":"1"`, `"launch_template_version":"$Latest"`, 1),
		strings.Replace(testutil.Manifest, "ami-12345678", "ubuntu-latest", 1),
		strings.Replace(testutil.Manifest, `"ubuntu_release":"24.04"`, `"ubuntu_release":"22.04"`, 1),
		strings.Replace(testutil.Manifest, `"owner_account":"099720109477"`, `"owner_account":"000000000000"`, 1),
		strings.Replace(testutil.Manifest, `"launch_template_version":"1"`, `"launch_template_version":"9999999999999999999999"`, 1),
		strings.Replace(testutil.Manifest, `"version":"1"`, `"version":"$Latest"`, 1),
		strings.Replace(testutil.Manifest, `"architecture":"x86_64"`, `"architecture":"arm64"`, 1),
		strings.Replace(testutil.Manifest, `"development_user":"devbox"`, `"development_user":"root"`, 1),
		strings.Replace(testutil.Manifest, "rtb-12345678", "SECRET", 1),
		strings.Replace(testutil.Manifest, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "SECRET", 1),
		strings.Replace(testutil.Manifest, "role/devbox-instance", "role/devbox-operator", 1),
		strings.Replace(testutil.Manifest, `"images":{"agent"`, `"images":{"missing"`, 1),
		testutil.Manifest + `{ "secret": "SECRET" }`,
		strings.Replace(testutil.Manifest, `"schema_version":3`, `"secret":"SECRET","schema_version":3`, 1),
	} {
		testutil.Write(t, c.Manifest, data)
		_, err := LoadManifest(c.Manifest, c, p)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("expected safe manifest rejection: %v", err)
		}
	}
}
