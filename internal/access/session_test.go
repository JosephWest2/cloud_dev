package access

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	et "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

type accessEC2 struct {
	*ec2.Client
	wrong bool
}

func (a *accessEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	owner := "test-owner"
	if a.wrong {
		owner = "different"
	}
	return &ec2.DescribeInstancesOutput{Reservations: []et.Reservation{{OwnerId: aws.String("123456789012"), Instances: []et.Instance{{InstanceId: aws.String("i-12345678"), State: &et.InstanceState{Name: et.InstanceStateNameRunning}, Tags: []et.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String("test")}, {Key: aws.String("Owner"), Value: aws.String(owner)}}}}}}}, nil
}

type sessionAPI struct {
	*ssm.Client
	starts, ends int
	cleanupErr   bool
}

func (a *sessionAPI) StartSession(_ context.Context, in *ssm.StartSessionInput, _ ...func(*ssm.Options)) (*ssm.StartSessionOutput, error) {
	if aws.ToString(in.Target) != "i-12345678" || aws.ToString(in.DocumentName) != "AWS-StartSSHSession" || len(in.Parameters) != 1 || strings.Join(in.Parameters["portNumber"], ",") != "22" {
		panic("unsafe session")
	}
	a.starts++
	return &ssm.StartSessionOutput{SessionId: aws.String("devbox-test-session"), StreamUrl: aws.String("wss://example.invalid"), TokenValue: aws.String("SECRET_TOKEN")}, nil
}
func (a *sessionAPI) TerminateSession(ctx context.Context, in *ssm.TerminateSessionInput, _ ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error) {
	if ctx.Err() != nil || aws.ToString(in.SessionId) != "devbox-test-session" {
		panic("cleanup lost identity/context")
	}
	a.ends++
	if a.cleanupErr {
		return nil, errors.New("SECRET_CLEANUP")
	}
	return &ssm.TerminateSessionOutput{}, nil
}
func TestProxyScopeTokenHandoffAndCleanup(t *testing.T) {
	for _, variant := range []string{"success", "startup failure", "timeout", "scope", "cleanup failure"} {
		t.Run(variant, func(t *testing.T) {
			testutil.IsolateAWS(t)
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args")
			envFile := filepath.Join(dir, "env")
			behavior := "printf 'SSH-2.0-test\\r\\nbytes'"
			if variant == "startup failure" {
				behavior = "echo SECRET_STARTUP; echo SECRET_ERROR >&2; exit 1"
			}
			if variant == "timeout" {
				behavior = "sleep 20"
			}
			plugin := testutil.Write(t, filepath.Join(dir, "session-manager-plugin"), "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.2.835.0; exit 0; fi\nprintf '%s\\n' \"$@\" > "+ShellQuote(argsFile)+"\nprintf '%s' \"$AWS_SSM_START_SESSION_RESPONSE_DEVBOX\" > "+ShellQuote(envFile)+"\n"+behavior+"\n")
			os.Chmod(plugin, 0700)
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			c := config.Config{ExpectedAccount: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "test-owner", AWSProfile: "test"}
			api := &sessionAPI{cleanupErr: variant == "cleanup failure"}
			s := &lifecycle.Service{Scope: c, API: &accessEC2{wrong: variant == "scope"}, SSM: api}
			setup, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			var out, diag bytes.Buffer
			code, err := proxy(context.Background(), setup, s, c, "i-12345678", strings.NewReader(""), &out, &diag)
			if variant == "scope" {
				if err == nil || api.starts != 0 || api.ends != 0 {
					t.Fatal("out of scope session")
				}
				return
			}
			if api.starts != 1 || api.ends != 1 {
				t.Fatalf("session leak: %+v %v", api, err)
			}
			args, _ := os.ReadFile(argsFile)
			env, _ := os.ReadFile(envFile)
			if bytes.Contains(args, []byte("SECRET")) || !bytes.Contains(env, []byte("SECRET_TOKEN")) || strings.Contains(out.String()+diag.String(), "SECRET") {
				t.Fatalf("unsafe handoff/output %q %q", args, diag.String())
			}
			if variant == "success" || variant == "cleanup failure" {
				if code != 0 || err != nil || out.String() != "SSH-2.0-test\r\nbytes" {
					t.Fatalf("%d %v %q", code, err, out.String())
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if variant == "cleanup failure" && !strings.Contains(diag.String(), "devbox-test-session") {
				t.Fatal("cleanup ID lost")
			}
		})
	}
}
func TestInstalledPluginVersionAndStartupAdapter(t *testing.T) {
	path, err := exec.LookPath("session-manager-plugin")
	if err != nil {
		t.Skip("installed plugin unavailable")
	}
	testutil.IsolateAWS(t)
	t.Setenv("AWS_CONFIG_FILE", testutil.Write(t, filepath.Join(t.TempDir(), "config"), "[profile test]\ncredential_process = sh -c 'echo SECRET_PROVIDER >&2; echo SECRET_PROVIDER; exit 1'\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err = CheckPlugin(ctx); err != nil {
		t.Fatal(err)
	}
	response := []byte(`{"SessionId":"devbox-test-session","StreamUrl":"wss://127.0.0.1:1","TokenValue":"SECRET_TOKEN"}`)
	cmd := exec.CommandContext(ctx, path, responseEnv, "us-east-2", "StartSession", "test", `{"Target":"i-12345678"}`, "https://127.0.0.1:1")
	cmd.Env = childEnv(response)
	var raw bytes.Buffer
	cmd.Stdout = &raw
	cmd.Stderr = io.Discard
	cmd.Run()
	var out bytes.Buffer
	if err = copySSH(&out, bytes.NewReader(raw.Bytes())); err == nil || out.Len() != 0 {
		t.Fatal("real plugin startup error entered SSH stream")
	}
}
