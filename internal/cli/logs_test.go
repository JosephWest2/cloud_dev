package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/execution"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/logs"
)

const cliLogsID = "dc1-0123456789abcdef0123456789abcdef"

func TestLogsCLISelectionAndMetadataNeverMixWithBytes(t *testing.T) {
	for _, mode := range []string{"status", "json", "stream", "export"} {
		t.Run(mode, func(t *testing.T) {
			var out, diag bytes.Buffer
			payload := []byte{'S', 'E', 'C', 'R', 'E', 'T', 0, 0xff, '\n'}
			args := []string{"--config", "/selected/config.toml", "--aws-profile", "operator", "logs", cliLogsID, "--region", "us-east-2"}
			switch mode {
			case "json":
				args = append(args, "--json")
			case "stream":
				args = append(args, "--stream", "stderr")
			case "export":
				args = append(args, "--stdout-file=stdout.bin", "--stderr-file", "stderr.bin", "--json", "--timeout", "2m")
			}
			calls := 0
			code := runWithCommands(context.Background(), args, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{}, nil, func(_ context.Context, path string, overrides config.Overrides, options logs.Options, output io.Writer) logs.Result {
				calls++
				if path != "/selected/config.toml" || overrides.AWSProfile != "operator" || overrides.Region != "us-east-2" || options.CommandID != cliLogsID {
					t.Fatal("logs scope was not forwarded")
				}
				if mode == "export" {
					if options.StdoutFile != "stdout.bin" || options.StderrFile != "stderr.bin" || options.Timeout != 2*time.Minute {
						t.Fatal("file export options lost")
					}
				} else if options.Timeout != 20*time.Second {
					t.Fatal("logs default deadline changed")
				}
				if mode == "stream" {
					if options.Stream != "stderr" {
						t.Fatal("wrong stream selection")
					}
					_, _ = output.Write(payload)
				}
				remoteExit := 255
				return logs.Result{Result: execution.Result{SchemaVersion: 1, Command: "logs", OK: true, ExitCode: 0, Outcome: "complete", CommandID: cliLogsID, Workload: &execprotocol.Workload{Status: "exited", ExitCode: &remoteExit}, Capture: "complete", Publication: "complete"}, Encoding: "bytes", Verification: "not_downloaded"}
			})
			if code != 0 || calls != 1 {
				t.Fatalf("retrieval used workload exit: code=%d calls=%d", code, calls)
			}
			if mode == "stream" {
				if !bytes.Equal(out.Bytes(), payload) || strings.Contains(diag.String(), "SECRET") || !strings.Contains(diag.String(), "remote_exit_code=255") || !strings.Contains(diag.String(), "verification=not_downloaded") {
					t.Fatal("stream bytes and metadata mixed")
				}
			} else {
				if strings.Contains(out.String()+diag.String(), "SECRET") {
					t.Fatal("metadata mode exposed workload bytes")
				}
				if mode == "json" || mode == "export" {
					var result logs.Result
					decoder := json.NewDecoder(&out)
					if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result.Command != "logs" || result.ExitCode != 0 || result.Workload == nil || *result.Workload.ExitCode != 255 || result.Encoding != "bytes" || result.Verification != "not_downloaded" {
						t.Fatal("invalid logs JSON envelope")
					}
				} else if !strings.Contains(out.String(), "remote_exit_code=255") {
					t.Fatal("text lost distinct workload exit")
				}
			}
		})
	}
}

func TestLogsUsageRejectedBeforeRetrieval(t *testing.T) {
	for _, args := range [][]string{
		{"logs"}, {"logs", "SECRET"}, {"logs", cliLogsID, cliLogsID},
		{"logs", cliLogsID, "--stream", "both"}, {"logs", cliLogsID, "--stream", "stdout", "--json"},
		{"logs", cliLogsID, "--stream", "stderr", "--stdout-file", "SECRET"},
		{"logs", cliLogsID, "--stdout-file="}, {"logs", cliLogsID, "--stderr-file"},
		{"logs", cliLogsID, "--cwd", "SECRET"}, {"logs", cliLogsID, "--", "SECRET"},
		{"logs", cliLogsID, "--wait-timeout", "1s"}, {"logs", cliLogsID, "--timeout", "6m"},
		{"logs", cliLogsID, "--name", "SECRET"}, {"ls", "--stream", "stdout"},
		{"exec", "smoke", "--stdout-file", "SECRET", "--", "true"},
	} {
		var out, diag bytes.Buffer
		code := runWithCommands(context.Background(), append([]string{"--json"}, args...), &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{}, nil, func(context.Context, string, config.Overrides, logs.Options, io.Writer) logs.Result {
			t.Fatal("invalid usage reached retrieval")
			return logs.Result{}
		})
		if code != 2 || !json.Valid(out.Bytes()) || strings.Contains(out.String()+diag.String(), "SECRET") {
			t.Fatalf("unsafe invalid usage: code=%d out=%s diag=%s", code, &out, &diag)
		}
	}
	for _, args := range [][]string{
		{"logs", "invalid", "--stream", "stdout"},
		{"logs", cliLogsID, "--timeout", "nonsense", "--stream", "stdout"},
		{"logs", cliLogsID, "--unknown", "--stream=stderr"},
		{"--timeout", "nonsense", "logs", cliLogsID, "--stream=stderr"},
	} {
		var out, diag bytes.Buffer
		code := Run(context.Background(), args, &out, &diag, doctor.Dependencies{})
		if code != 2 || out.Len() != 0 || diag.Len() == 0 {
			t.Fatal("stream-mode validation polluted stdout")
		}
	}
}

func TestLogsJSONPreservesFailureMetadataAndVerifiedFirstExport(t *testing.T) {
	remote := 4
	r := logs.Result{
		Result:   execution.Result{SchemaVersion: 1, Command: "logs", ExitCode: 1, Outcome: "access_denied", Code: "output_access_denied", Message: "output access denied", CommandID: cliLogsID, Workload: &execprotocol.Workload{Status: "exited", ExitCode: &remote}, Publication: "complete"},
		Encoding: "bytes", Verification: "failed", Downloads: &logs.Downloads{Stdout: &logs.Download{Destination: "file", File: "first.bin", BytesWritten: 100, Verification: "verified"}, Stderr: &logs.Download{Destination: "file", Verification: "failed"}},
	}
	var out, diag bytes.Buffer
	if emitLogs(r, true, false, &out, &diag) != 1 {
		t.Fatal("retrieval failure exit was replaced")
	}
	var got logs.Result
	decoder := json.NewDecoder(&out)
	if decoder.Decode(&got) != nil || decoder.Decode(new(any)) != io.EOF || got.Workload == nil || *got.Workload.ExitCode != 4 || got.Downloads.Stdout.File != "first.bin" || got.Downloads.Stderr.File != "" || got.Verification != "failed" {
		t.Fatal("JSON lost partial export evidence or known workload status")
	}
}
