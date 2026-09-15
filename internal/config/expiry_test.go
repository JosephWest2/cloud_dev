package config

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestTTLConfigPresenceAndLaunchIndependentLoading(t *testing.T) {
	for _, value := range []string{"", "default_ttl='2h'\n", "default_ttl=''\n", "default_ttl='unlimited'\n"} {
		path := testutil.Setup(t)
		testutil.Write(t, path, testutil.Config+value)
		c, err := Load(path, Overrides{})
		if err != nil {
			t.Fatalf("launch duration blocked scope loading: %v", err)
		}
		if (c.DefaultTTL == nil) != (value == "") {
			t.Fatal("TTL omission lost")
		}
		if _, err := LoadResultManifest(c.Manifest, c); err != nil {
			t.Fatal("TTL blocked logs manifest", err)
		}
	}
	path := testutil.Setup(t)
	testutil.Write(t, path, testutil.Config+"default_ttl=2\n")
	if _, err := Load(path, Overrides{}); err == nil {
		t.Fatal("wrong TTL type accepted")
	}
}

func TestManifestV6CleanupDoesNotGateRecovery(t *testing.T) {
	path := testutil.Setup(t)
	c, err := Load(path, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	// Result retrieval validates storage scope without needing launch resources or
	// cleanup health. #46 owns the opaque descriptor's nested schema and verifier.
	for _, cleanup := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{"execution_role":null,"evidence":{"schema_version":1}}`)} {
		m.SchemaVersion = 6
		m.Cleanup = cleanup
		data, _ := json.Marshal(m)
		testutil.Write(t, filepath.Join(filepath.Dir(path), "deployment.json"), string(data))
		if _, err := LoadResultManifest(c.Manifest, c); err != nil {
			t.Fatal("cleanup health blocked saved logs", err)
		}
	}
	m.SchemaVersion = 5
	data, _ := json.Marshal(m)
	testutil.Write(t, c.Manifest, string(data))
	if _, err := LoadResultManifest(c.Manifest, c); err == nil {
		t.Fatal("v5 accepted v6-only descriptor")
	}
}
