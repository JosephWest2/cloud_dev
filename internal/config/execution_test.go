package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestExecutionManifestWithoutLocalLaunchOrSSHDependencies(t *testing.T) {
	path := testutil.Setup(t)
	testutil.Write(t, path, testutil.Config+"profile_file = 'missing-profile.toml'\nssh_identity_file = 'missing-key'\n")
	// An exec-only workstation does not need OpenSSH or the Session Manager
	// plugin, nor the profile and private key used when launching the worker.
	t.Setenv("PATH", "")
	c, err := Load(path, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.ProfileFile != filepath.Join(filepath.Dir(path), "missing-profile.toml") || c.SSHIdentityFile != filepath.Join(filepath.Dir(path), "missing-key") {
		t.Fatal("fixture did not select missing local dependencies")
	}
	if _, err := LoadProfile(c.ProfileFile); err == nil {
		t.Fatal("fixture launch profile unexpectedly exists")
	}
	m, err := LoadExecutionManifest(c.Manifest, c)
	if err != nil {
		t.Fatal(err)
	}
	image := m.Images["agent"]
	if image.AMIID != "ami-12345678" || image.LaunchTemplateID != "lt-12345678" || image.LaunchTemplateVersion != "1" || m.Execution.Step != "execute" || m.Results.Prefix != ResultsPrefix(c) {
		t.Fatalf("execution target pins missing: %+v", m)
	}
}

func TestExecutionManifestRequiresScopedCompleteRuntimePins(t *testing.T) {
	c, err := Load(testutil.Setup(t), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"old schema", func(m *Manifest) { m.SchemaVersion = 3 }},
		{"wrong account", func(m *Manifest) { m.Account = "000000000000" }},
		{"wrong region", func(m *Manifest) { m.Region = "us-west-2" }},
		{"wrong deployment", func(m *Manifest) { m.Deployment = "other" }},
		{"wrong owner", func(m *Manifest) { m.Owner = "other" }},
		{"missing network", func(m *Manifest) { m.VPCID = "" }},
		{"missing instance profile", func(m *Manifest) { m.InstanceProfileARN = "" }},
		{"missing IAM pins", func(m *Manifest) { m.Roles = nil }},
		{"missing bootstrap pin", func(m *Manifest) { m.BootstrapSHA256 = "" }},
		{"missing readiness", func(m *Manifest) { m.Readiness = Document{} }},
		{"missing execution", func(m *Manifest) { m.Execution = Execution{} }},
		{"missing runner pin", func(m *Manifest) { m.Execution.RunnerSHA256 = "" }},
		{"mutable execution version", func(m *Manifest) { m.Execution.Version = "$Latest" }},
		{"missing storage", func(m *Manifest) { m.Results = Results{} }},
		{"wrong storage scope", func(m *Manifest) { m.Results.Prefix += "other/" }},
		{"missing image", func(m *Manifest) { m.Images = nil }},
		{"wrong image name", func(m *Manifest) { m.Images = map[string]Image{"other": m.Images["agent"]} }},
		{"missing AMI pin", func(m *Manifest) { image := m.Images["agent"]; image.AMIID = ""; m.Images["agent"] = image }},
		{"mutable template version", func(m *Manifest) {
			image := m.Images["agent"]
			image.LaunchTemplateVersion = "$Default"
			m.Images["agent"] = image
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m Manifest
			if err := json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
				t.Fatal(err)
			}
			tc.change(&m)
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			testutil.Write(t, c.Manifest, string(data))
			if _, err := LoadExecutionManifest(c.Manifest, c); err == nil {
				t.Fatal("incomplete or mismatched execution manifest accepted")
			}
		})
	}
	testutil.Write(t, c.Manifest, strings.Replace(testutil.Manifest, `"schema_version":4`, `"secret":"SECRET","schema_version":4`, 1))
	if _, err := LoadExecutionManifest(c.Manifest, c); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("expected redacted unknown-field rejection: %v", err)
	}
}
