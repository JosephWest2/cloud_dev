package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestDoctorStructuredResults(t *testing.T) {
	for _, tc := range []struct {
		name                string
		alter               func(*testing.T, string)
		identityErr         error
		pluginErr           error
		sshErr              error
		wantExit, wantCalls int
	}{
		{name: "ready", wantExit: 0, wantCalls: 1},
		{name: "missing config", alter: func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}, wantExit: 2},
		{name: "malformed config", alter: func(t *testing.T, p string) { testutil.Write(t, p, `secret = "SECRET`) }, wantExit: 2},
		{name: "unsupported config", alter: func(t *testing.T, p string) {
			testutil.Write(t, p, strings.Replace(testutil.Config, "schema_version = 1", "schema_version = 99", 1))
		}, wantExit: 2},
		{name: "invalid profile", alter: func(t *testing.T, p string) {
			testutil.Write(t, p, testutil.Config+"profile_file='agent.toml'\n")
			testutil.Write(t, filepath.Join(filepath.Dir(p), "agent.toml"), "secret='SECRET'\n")
		}, wantExit: 2},
		{name: "missing manifest", alter: func(t *testing.T, p string) {
			if err := os.Remove(filepath.Join(filepath.Dir(p), "deployment.json")); err != nil {
				t.Fatal(err)
			}
		}, wantExit: 1, wantCalls: 1},
		{name: "provider secret", identityErr: errors.New("SECRET"), wantExit: 1, wantCalls: 1},
		{name: "expired credentials", identityErr: &identity.Failure{Code: "credentials_expired", Message: "AWS credentials expired; refresh the selected profile"}, wantExit: 1, wantCalls: 1},
		{name: "unexpected account", identityErr: &identity.Failure{Code: "account_mismatch", Message: "select the intended AWS profile"}, wantExit: 1, wantCalls: 1},
		{name: "deadline", identityErr: &identity.Failure{Code: "timeout", Message: "check connectivity"}, wantExit: 4, wantCalls: 1},
		{name: "missing plugin", pluginErr: errors.New("SECRET"), wantExit: 1, wantCalls: 1},
		{name: "missing SSH", sshErr: errors.New("SECRET"), wantExit: 1, wantCalls: 1},
		{name: "plugin timeout", pluginErr: context.DeadlineExceeded, wantExit: 4, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AWS_PROFILE", "")
			path := testutil.Setup(t)
			if tc.alter != nil {
				tc.alter(t, path)
			}
			calls, pluginCalls := 0, 0
			deps := doctor.Dependencies{GOOS: "linux", Identity: func(context.Context, config.Config) error { calls++; return tc.identityErr }, Plugin: func(context.Context) error { pluginCalls++; return tc.pluginErr }, SSH: func(context.Context) error { return tc.sshErr }}
			var out, diag bytes.Buffer
			code := Run(context.Background(), []string{"doctor", "--config", path, "--json"}, &out, &diag, deps)
			if code != tc.wantExit || calls != tc.wantCalls || pluginCalls != 1 {
				t.Fatalf("exit/calls: %d/%d/%d; output %s", code, calls, pluginCalls, out.String())
			}
			var r doctor.Result
			d := json.NewDecoder(&out)
			if err := d.Decode(&r); err != nil {
				t.Fatal(err)
			}
			if d.Decode(new(any)) != io.EOF {
				t.Fatal("stdout must contain exactly one JSON object")
			}
			if r.ExitCode != code || r.OK != (code == 0) || r.SchemaVersion != 1 || r.Command != "doctor" {
				t.Fatalf("bad envelope: %+v", r)
			}
			encoded, _ := json.Marshal(r)
			if strings.Contains(string(encoded)+diag.String(), "SECRET") {
				t.Fatal("secret leaked")
			}
			if (diag.Len() > 0) != (code != 0) {
				t.Fatal("unexpected diagnostic stream")
			}
		})
	}
}

func TestUsageRedactionAndJSON(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "--bad=SECRET", "--json"},
		{"--json", "SECRET"},
		{"doctor", "--config", "--json"},
		{"doctor", "--timeout=SECRET", "--json"},
		{"doctor", "--timeout=0s", "--json"},
		{"doctor", "--timeout=6m", "--json"},
		{"doctor", "--aws-profile=", "--json"},
	} {
		var out, diag bytes.Buffer
		code := Run(context.Background(), args, &out, &diag, doctor.Dependencies{})
		if code != 2 || !json.Valid(out.Bytes()) || strings.Contains(out.String()+diag.String(), "SECRET") {
			t.Fatalf("invalid usage result: %d %s %s", code, &out, &diag)
		}
	}
	for _, args := range [][]string{{"--json", "--help"}, {"version", "--json"}} {
		var out, diag bytes.Buffer
		if code := Run(context.Background(), args, &out, &diag, doctor.Dependencies{}); code != 0 || !json.Valid(out.Bytes()) || diag.Len() != 0 {
			t.Fatalf("metadata output: %d %s %s", code, &out, &diag)
		}
	}
}

func TestDoctorDeadline(t *testing.T) {
	deps := doctor.Dependencies{GOOS: "linux", Identity: func(ctx context.Context, _ config.Config) error {
		<-ctx.Done()
		return &identity.Failure{Code: "timeout", Message: "retry"}
	}, Plugin: func(context.Context) error { t.Fatal("probe must not run after the doctor deadline"); return nil }, SSH: func(context.Context) error { t.Fatal("probe must not run after the doctor deadline"); return nil }}
	var out, diag bytes.Buffer
	if code := Run(context.Background(), []string{"--timeout", "1ms", "--config", testutil.Setup(t), "doctor", "--json"}, &out, &diag, deps); code != 4 {
		t.Fatalf("expected deadline exit: %d %s", code, &out)
	}
	var r doctor.Result
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	for _, check := range r.Checks {
		if check.Name == "session_manager_plugin" || check.Name == "openssh_client" {
			if check.Status != "skip" || check.Code != "check_canceled" {
				t.Fatalf("deadline falsely diagnosed installation: %+v", check)
			}
		}
	}
}
