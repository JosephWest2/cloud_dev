package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

type ttlClock struct{}

func (ttlClock) Now() time.Time { return time.Date(2026, 9, 14, 0, 0, 0, 123456789, time.UTC) }

func TestTTLCLIValidationPrecedenceAndStructuredPreview(t *testing.T) {
	for _, tc := range []struct {
		name, configured string
		args             []string
		want             string
	}{
		{name: "default", want: "2h0m0s"},
		{name: "configured", configured: "default_ttl='36h'\n", want: "36h0m0s"},
		{name: "override", configured: "default_ttl='36h'\n", args: []string{"--ttl", "1h30m"}, want: "1h30m0s"},
		{name: "equals", args: []string{"--ttl=168h"}, want: "168h0m0s"},
		{name: "empty", args: []string{"--ttl="}},
		{name: "invalid-default-overridden", configured: "default_ttl='unlimited'\n", args: []string{"--ttl", "2h"}},
		{name: "zero", args: []string{"--ttl", "0s"}},
		{name: "days", args: []string{"--ttl", "7d"}},
		{name: "too-long", args: []string{"--ttl", "168h1ns"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := cliBatchConfig(t, 10)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			testutil.Write(t, path, string(data)+tc.configured)
			t.Setenv("DEVBOX_TTL", "1ns")
			t.Setenv("DEFAULT_TTL", "1ns")
			calls := 0
			var out, diag bytes.Buffer
			args := append([]string{"up", "agent", "--config", path, "--json"}, tc.args...)
			code := RunWithLifecycle(context.Background(), args, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{Clock: ttlClock{}, Store: &lifecycle.Store{Dir: t.TempDir()}, New: func(context.Context, config.Config) (*lifecycle.Service, error) {
				calls++
				return nil, errors.New("controlled stop before allocation")
			}})
			r := decodeCLIBatch(t, out.Bytes())
			if tc.want == "" {
				if code != 2 || r.Code != "ttl_invalid" || calls != 0 {
					t.Fatalf("invalid TTL: %+v calls=%d", r, calls)
				}
				return
			}
			if code != 1 || calls != 1 || r.TTL == nil || *r.TTL != tc.want || r.ExpiresAt == nil || *r.ExpiresAt != r.Plan.ExpiresAt || r.Plan.CreatedAt != "2026-09-14T00:00:00.123456789Z" {
				t.Fatalf("wrong TTL output: %+v", r)
			}
		})
	}
}

func TestTTLReplayOverrideRejectedBeforeConfigOrAWS(t *testing.T) {
	for _, recovery := range [][]string{{"--resume", strings.Repeat("a", 32)}, {"--retry-missing", strings.Repeat("a", 32), "--after", strings.Repeat("b", 32)}} {
		for _, override := range []string{"--ttl=2h", "--ttl=", "--ttl=invalid", "--name=agent", "--count=1"} {
			var out, diag bytes.Buffer
			args := append([]string{"up", "--json", "--config", "/missing"}, recovery...)
			args = append(args, override)
			code := RunWithLifecycle(context.Background(), args, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{New: func(context.Context, config.Config) (*lifecycle.Service, error) {
				t.Fatal("replay reached AWS")
				return nil, nil
			}})
			r := decodeCLIBatch(t, out.Bytes())
			if code != 2 || r.Code != "replay_override" {
				t.Fatalf("replay: %+v", r)
			}
		}
	}
}

func TestExpiryPublicTextAndJSONKeepLegacyAndMalformedWorkers(t *testing.T) {
	for _, status := range []string{"future", "expired", "missing", "invalid", "duplicate"} {
		i := lifecycle.Instance{ID: "i-12345678", ExpiryStatus: status, Volumes: []lifecycle.Volume{}}
		if status == "future" || status == "expired" {
			i.ExpiresAt = "2026-09-14T02:00:00Z"
		}
		r := lifecycle.Result{SchemaVersion: 2, Command: "ls", OK: true, Outcome: lifecycle.Outcome{Instances: []lifecycle.Instance{i}}}
		for _, jsonMode := range []bool{false, true} {
			var out, diag bytes.Buffer
			if code := emitLifecycle(r, jsonMode, &out, &diag); code != 0 {
				t.Fatal(code)
			}
			if jsonMode {
				var result struct {
					Instances []map[string]any `json:"instances"`
				}
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Instances) != 1 || result.Instances[0]["expiry_status"] != status {
					t.Fatal(out.String())
				}
				value, exists := result.Instances[0]["expires_at"]
				if !exists || (i.ExpiresAt == "" && value != nil) {
					t.Fatal(out.String())
				}
			} else if !strings.Contains(out.String(), "expiry_status="+status) || !strings.Contains(out.String(), "expires_at="+expiryText(i.ExpiresAt)) {
				t.Fatal(out.String())
			}
		}
	}
}
