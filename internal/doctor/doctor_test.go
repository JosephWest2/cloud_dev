package doctor

import (
	"context"
	"errors"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/foundation"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestCanceledProbePreservesContextError(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ssh", "session-manager-plugin"} {
		path := testutil.Write(t, filepath.Join(dir, name), "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, check := range []func(context.Context) error{CheckSSH, CheckPlugin} {
		if err := check(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, not installation failure: %v", err)
		}
	}
}

func TestFoundationGatedByManifestAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		manifest    string
		identityErr error
		wantCalls   int
	}{
		{name: "valid", manifest: testutil.Manifest, wantCalls: 1},
		{name: "wrong account", manifest: strings.Replace(testutil.Manifest, "123456789012", "000000000000", 1)},
		{name: "old schema", manifest: strings.Replace(testutil.Manifest, `"schema_version":3`, `"schema_version":1`, 1)},
		{name: "unverified identity", manifest: testutil.Manifest, identityErr: errors.New("SECRET")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := testutil.Setup(t)
			testutil.Write(t, filepath.Join(filepath.Dir(path), "deployment.json"), tc.manifest)
			calls := 0
			deps := Dependencies{GOOS: "linux", Identity: func(context.Context, config.Config) error { return tc.identityErr }, Plugin: func(context.Context) error { return nil }, SSH: func(context.Context) error { return nil }, Foundation: func(context.Context, config.Config, config.Manifest, config.Profile) []foundation.Check {
				calls++
				return []foundation.Check{{Name: "foundation_template"}}
			}}
			r := Run(context.Background(), path, config.Overrides{}, deps)
			if calls != tc.wantCalls || r.OK != (tc.wantCalls == 1) {
				t.Fatalf("calls=%d result=%+v", calls, r)
			}
		})
	}
}

func TestFoundationErrorsRedactedAndTimeoutClassified(t *testing.T) {
	for _, failure := range []error{errors.New("SECRET"), context.DeadlineExceeded} {
		deps := Dependencies{GOOS: "linux", Identity: func(context.Context, config.Config) error { return nil }, Plugin: func(context.Context) error { return nil }, SSH: func(context.Context) error { return nil }, Foundation: func(context.Context, config.Config, config.Manifest, config.Profile) []foundation.Check {
			return []foundation.Check{{Name: "foundation_template", Err: failure}}
		}}
		r := Run(context.Background(), testutil.Setup(t), config.Overrides{}, deps)
		expected := ExitPrerequisite
		if errors.Is(failure, context.DeadlineExceeded) {
			expected = ExitTimeout
		}
		if r.ExitCode != expected {
			t.Fatal(r)
		}
		for _, check := range r.Checks {
			if strings.Contains(check.Message, "SECRET") {
				t.Fatal("leaked dependency error")
			}
		}
	}
}
