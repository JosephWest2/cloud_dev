package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/execution"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
)

func TestExecSeparatorPreservesAllRemoteArguments(t *testing.T) {
	remote := []string{"/usr/bin/printf", "", "two words", "a\nb", "snowman-☃", "quote'\"", "$(id); touch SECRET", "--json", "--help", "--timeout", "garbage", "--", "-h"}
	for _, localJSON := range []bool{false, true} {
		var out, diag bytes.Buffer
		calls := 0
		args := []string{"--config", "/selected/config.toml", "--region", "us-east-2", "exec", "smoke", "--aws-profile=operator", "--cwd", "../project", "--timeout=3s", "--exec-timeout=90s", "--delivery-timeout", "45s", "--wait-timeout=2m"}
		if localJSON {
			args = append(args, "--json")
		}
		args = append(append(args, "--"), remote...)
		runner := func(_ context.Context, path string, overrides config.Overrides, o execution.RunOptions, progress io.Writer) execution.Result {
			calls++
			if path != "/selected/config.toml" || overrides.AWSProfile != "operator" || overrides.Region != "us-east-2" || o.Target != "smoke" || o.Cwd != "../project" || !reflect.DeepEqual(o.Argv, remote) {
				t.Fatalf("local/remote boundary changed: path=%q overrides=%+v options=%+v", path, overrides, o)
			}
			if o.SetupTimeout != 3*time.Second || o.ExecTimeout != 90*time.Second || o.DeliveryTimeout != 45*time.Second || o.WaitTimeout != 2*time.Minute {
				t.Fatal("independent clocks changed")
			}
			fmt.Fprintln(progress, "submitted command_id=dc1-0123456789abcdef0123456789abcdef")
			code := 4
			return execution.Result{SchemaVersion: 1, Command: "exec", Outcome: "remote_exit", ExitCode: code, CommandID: "dc1-0123456789abcdef0123456789abcdef", Workload: &execprotocol.Workload{Status: "exited", ExitCode: &code}, Publication: "complete", RecoveryCommand: "devbox logs dc1-0123456789abcdef0123456789abcdef"}
		}
		code := runWithExecution(context.Background(), args, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{}, runner)
		if code != 4 || calls != 1 || json.Valid(out.Bytes()) != localJSON {
			t.Fatalf("exit=%d calls=%d out=%s", code, calls, &out)
		}
		if strings.Contains(out.String(), "submitted") || !strings.Contains(diag.String(), "submitted command_id=") || strings.Contains(out.String()+diag.String(), "SECRET") {
			t.Fatal("progress or remote data escaped its output boundary")
		}
		if localJSON {
			var result execution.Result
			decoder := json.NewDecoder(&out)
			if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result.Outcome != "remote_exit" || result.Workload.ExitCode == nil || *result.Workload.ExitCode != 4 {
				t.Fatal("invalid execution envelope")
			}
		} else if !strings.Contains(out.String(), "remote_exit_code=4 publication=complete") {
			t.Fatal("text did not identify remote exit")
		}
	}
}

func TestExecRemoteJSONCannotChangeInvalidUsageFormat(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "smoke", "--bad=SECRET", "--", "--json"},
		{"exec", "--", "echo", "--json"},
		{"exec", "smoke", "--", "", "--json"},
		{"doctor", "--", "--json"},
	} {
		var out, diag bytes.Buffer
		code := Run(context.Background(), args, &out, &diag, doctor.Dependencies{})
		if code != 2 || json.Valid(out.Bytes()) || strings.Contains(out.String()+diag.String(), "SECRET") {
			t.Fatalf("remote JSON affected local failure: %d %s %s", code, &out, &diag)
		}
	}
}

func TestExecUsageRejectedBeforeExecution(t *testing.T) {
	cases := [][]string{
		{"exec", "smoke", "echo"}, {"exec", "smoke", "--"}, {"exec", "smoke", "other", "--", "echo"},
		{"exec", "smoke", "--resume", "0123456789abcdef0123456789abcdef", "--", "echo"},
		{"exec", "smoke", "--name", "SECRET", "--", "echo"},
		{"exec", "smoke", "--cwd=", "--", "echo"},
		{"exec", "smoke", "--cwd", "--", "echo"},
		{"exec", "smoke", "--exec-timeout", "500ms", "--", "echo"},
		{"exec", "smoke", "--exec-timeout", "25h", "--", "echo"},
		{"exec", "smoke", "--delivery-timeout", "29s", "--", "echo"},
		{"exec", "smoke", "--delivery-timeout", "30.5s", "--", "echo"},
		{"exec", "smoke", "--wait-timeout", "0s", "--", "echo"},
		{"exec", "smoke", "--wait-timeout", "26h", "--", "echo"},
		{"exec", "smoke", "--timeout", "6m", "--", "echo"},
		{"ls", "--exec-timeout", "1h"}, {"doctor", "--cwd", "SECRET"},
	}
	for _, args := range cases {
		var out, diag bytes.Buffer
		args = append([]string{"--json"}, args...)
		code := runWithExecution(context.Background(), args, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{}, func(context.Context, string, config.Overrides, execution.RunOptions, io.Writer) execution.Result {
			t.Fatal("invalid usage reached execution")
			return execution.Result{}
		})
		if code != 2 || !json.Valid(out.Bytes()) || strings.Contains(out.String()+diag.String(), "SECRET") {
			t.Fatalf("unsafe invalid usage: %d %s %s", code, &out, &diag)
		}
	}
}

func TestExecLocalHelpAndClockDefaults(t *testing.T) {
	var out, diag bytes.Buffer
	code := runWithExecution(context.Background(), []string{"exec", "--help", "--", "--json"}, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{}, func(context.Context, string, config.Overrides, execution.RunOptions, io.Writer) execution.Result {
		t.Fatal("help reached execution")
		return execution.Result{}
	})
	if code != 0 || json.Valid(out.Bytes()) || !strings.Contains(out.String(), "--exec-timeout") {
		t.Fatal("local help boundary")
	}
	out.Reset()
	code = runWithExecution(context.Background(), []string{"exec", "smoke", "--", "--help"}, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{}, func(_ context.Context, _ string, _ config.Overrides, o execution.RunOptions, _ io.Writer) execution.Result {
		if o.SetupTimeout != 5*time.Minute || o.WaitTimeout != time.Hour || o.ExecTimeout != time.Hour || o.DeliveryTimeout != 5*time.Minute || !reflect.DeepEqual(o.Argv, []string{"--help"}) {
			t.Fatal("wrong defaults or remote help consumed")
		}
		return execution.Result{SchemaVersion: 1, Command: "exec", OK: true, Outcome: "remote_exit"}
	})
	if code != 0 {
		t.Fatal(code)
	}
}
