package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestPluralTeardownRequiresValidConfigurationBeforeAWS(t *testing.T) {
	for _, args := range [][]string{
		{"down", "worker1", "i-12345678"},
		{"down", "--group", "smoke-batch"},
		{"down", "--all"},
		{"down", "--all", "--yes"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			code := RunWithLifecycle(context.Background(), append(args, "--json", "--config", "/nonexistent/devbox.toml"), &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{New: func(context.Context, config.Config) (*lifecycle.Service, error) {
				t.Fatal("invalid configuration reached AWS")
				return nil, nil
			}})
			var result lifecycle.Result
			decoder := json.NewDecoder(&out)
			if err := decoder.Decode(&result); err != nil || decoder.Decode(new(any)) != io.EOF {
				t.Fatalf("expected one JSON result: %v", err)
			}
			if code != 2 || result.Code != "config_invalid" || result.OK || result.Command == "" || result.Instances == nil {
				t.Fatalf("selection must reject invalid config before AWS: exit=%d, result=%+v", code, result)
			}
		})
	}
}

func TestInvalidLifecycleSyntaxDoesNotReachAWS(t *testing.T) {
	request, attempt := strings.Repeat("a", 32), strings.Repeat("b", 32)
	cases := [][]string{
		{"up", "agent", "--count"},
		{"up", "agent", "--count="},
		{"up", "agent", "--count", "--group", "smoke"},
		{"up", "agent", "--count", "1", "--count", "2"},
		{"up", "agent", "--group", "first", "--group", "second"},
		{"up", "agent", "--name", "first", "--name", "second", "--on-demand"},
		{"up", "agent", "--on-demand", "--on-demand", "--name", "worker"},
		{"up", "agent", "--spot", "--on-demand"},
		{"up", "agent", "--all"},
		{"up", "agent", "--yes"},
		{"up", "agent", "--after", attempt},
		{"up", "--resume", request, "--retry-missing", request, "--after", attempt},
		{"up", "--resume", request, "--count", "1"},
		{"up", "--resume", request, "--group", "smoke"},
		{"up", "--resume", request, "--after", attempt},
		{"up", "--retry-missing", request},
		{"up", "--retry-missing", request, "--after", "invalid"},
		{"up", "agent", "--retry-missing", request, "--after", attempt},
		{"up", "--retry-missing", request, "--after", attempt, "--on-demand"},
		{"up", "--retry-missing", request, "--after", attempt, "--name", "worker"},
		{"up", "--retry-missing", request, "--after", attempt, "--group", "smoke"},
		{"ls", "--count", "2"},
		{"ls", "--yes"},
		{"ls", "--all"},
		{"ls", "--group", "smoke", "extra"},
		{"down"},
		{"down", "worker", "--yes"},
		{"down", "--group", "smoke", "--yes"},
		{"down", "worker", "--group", "smoke"},
		{"down", "worker", "--all"},
		{"down", "--group", "smoke", "--all"},
		{"down", "--all", "--all"},
		{"down", "--count", "2", "worker"},
		{"doctor", "--group", "smoke"},
	}
	for _, raw := range []string{"0", "-1", "+1", "1.5", "1e2", "101", " 2", "２", strings.Repeat("9", 100)} {
		cases = append(cases, []string{"up", "agent", "--count", raw, "--group", "smoke"})
	}
	for _, label := range []string{"bad/label", "bad label", "_label", "i-12345678", strings.Repeat("a", 64)} {
		cases = append(cases, []string{"up", "agent", "--group", label}, []string{"up", "agent", "--name", label}, []string{"ls", "--group", label}, []string{"down", "--group", label})
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			code := RunWithLifecycle(context.Background(), append(args, "--json", "--config", "/nonexistent/devbox.toml"), &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{New: func(context.Context, config.Config) (*lifecycle.Service, error) {
				t.Fatal("invalid syntax reached AWS")
				return nil, nil
			}})
			var result doctor.Result
			decoder := json.NewDecoder(&out)
			if err := decoder.Decode(&result); err != nil || decoder.Decode(new(any)) != io.EOF {
				t.Fatalf("expected one JSON result: %v", err)
			}
			if code != 2 || result.OK || len(result.Checks) != 1 || result.Checks[0].Code != "usage_invalid" {
				t.Fatalf("expected usage rejection: exit=%d, result=%+v", code, result)
			}
		})
	}
}

func TestLegacyLaunchAndResumeStillDispatch(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "named on-demand", true: "resume"}[resume], func(t *testing.T) {
			path := testutil.Setup(t)
			store := lifecycle.Store{Dir: t.TempDir()}
			args := []string{"up", "agent", "--on-demand", "--name", "worker"}
			if resume {
				request := strings.Repeat("a", 32)
				err := store.Save(lifecycle.Receipt{SchemaVersion: 1, RequestID: request, ClientToken: request, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), State: "dispatched", Parameters: lifecycle.Parameters{Account: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "alice", Profile: "agent", Name: "worker", Market: "on-demand", InstanceType: "m7i.large", DiskGB: 32, Image: config.Image{AMIID: "ami-12345678", LaunchTemplateID: "lt-12345678", LaunchTemplateVersion: "1"}}})
				if err != nil {
					t.Fatal(err)
				}
				args = []string{"up", "--resume", request}
			}
			calls := 0
			var out, diag bytes.Buffer
			code := RunWithLifecycle(context.Background(), append(args, "--json", "--config", path), &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{Store: &store, New: func(context.Context, config.Config) (*lifecycle.Service, error) {
				calls++
				return nil, errors.New("controlled factory failure")
			}})
			if code != 1 || calls != 1 {
				t.Fatalf("legacy dispatch changed: exit=%d, calls=%d, output=%s", code, calls, &out)
			}
		})
	}
}

func TestHelpExplainsBatchLaunchAndRecovery(t *testing.T) {
	var out, diag bytes.Buffer
	if code := Run(context.Background(), []string{"--help"}, &out, &diag, doctor.Dependencies{}); code != 0 {
		t.Fatalf("help failed: %d", code)
	}
	for _, expected := range []string{"Batch launch and recovery", "Scoped teardown", "up agent [--count N] [--group GROUP] [--name BASE]", "up --retry-missing REQUEST_ID --after ATTEMPT_ID", "down --all [--yes]", "configured maximum default 10", "one overall deadline", "without allocating"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("help omits %q", expected)
		}
	}
}
