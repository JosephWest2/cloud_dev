//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

// Exercise inherited OS descriptors through the real supervisor and worker:
// in-memory failing Writers do not trigger Go's fd-1/fd-2 SIGPIPE handling.
func TestCleanupBrokenInheritedPipesRetainRecovery(t *testing.T) {
	testutil.IsolateAWS(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "devbox")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	t.Setenv("AWS_CONFIG_FILE", testutil.Write(t, filepath.Join(dir, "aws-config"), "[profile selected]\nregion=us-west-2\n"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", testutil.Write(t, filepath.Join(dir, "credentials"), "[selected]\naws_access_key_id=FIXTURE_KEY\naws_secret_access_key=FIXTURE_SECRET\n"))
	// Explicit config profile/region must win even with contradictory environment
	// and broken unrelated launch settings.
	t.Setenv("AWS_ACCESS_KEY_ID", "WRONG_ENV_KEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "WRONG_ENV_SECRET")
	t.Setenv("AWS_REGION", "us-west-2")
	path := testutil.Write(t, filepath.Join(dir, "config.toml"), `schema_version=1
expected_account="123456789012"
region="us-east-2"
deployment="dev"
owner="joe"
aws_profile="selected"
manifest="missing.json"
profile_file="missing.toml"
ssh_identity_file="missing"
default_ttl={invalid=true}
max_count=["invalid"]
`)
	const instanceID = "i-00000001"
	const volumeID = "vol-00000001"
	const account = "123456789012"
	var mu sync.Mutex
	var actions []string
	state := "running"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "fixture", 500)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		auth := r.Header.Get("Authorization")
		if !strings.Contains(auth, "Credential=FIXTURE_KEY/") || !strings.Contains(auth, "/us-east-2/") {
			t.Errorf("incorrect credential/region selection: %s", auth)
		}
		action := r.Form.Get("Action")
		actions = append(actions, action)
		w.Header().Set("Content-Type", "text/xml")
		switch action {
		case "GetCallerIdentity":
			fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>%s</Account><Arn>arn:aws:iam::%s:user/test</Arn><UserId>test</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`, account, account)
		case "DescribeInstances":
			if id := r.Form.Get("InstanceId.1"); id != "" && id != instanceID {
				t.Errorf("unrelated exact instance %s", id)
			}
			if r.Form.Get("InstanceId.1") == "" {
				filters := map[string]string{}
				for i := 1; i <= 3; i++ {
					filters[r.Form.Get(fmt.Sprintf("Filter.%d.Name", i))] = r.Form.Get(fmt.Sprintf("Filter.%d.Value.1", i))
				}
				if len(filters) != 3 || filters["tag:ManagedBy"] != "devbox" || filters["tag:Deployment"] != "dev" || filters["tag:Owner"] != "joe" {
					t.Errorf("unsafe discovery filters: %+v", filters)
				}
			}
			fmt.Fprintf(w, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservationSet><item><ownerId>%s</ownerId><instancesSet><item><instanceId>%s</instanceId><instanceState><name>%s</name></instanceState><placement><availabilityZone>us-east-2a</availabilityZone></placement><rootDeviceName>/dev/xvda</rootDeviceName><rootDeviceType>ebs</rootDeviceType><blockDeviceMapping><item><deviceName>/dev/xvda</deviceName><ebs><volumeId>%s</volumeId><deleteOnTermination>true</deleteOnTermination></ebs></item></blockDeviceMapping><tagSet><item><key>ManagedBy</key><value>devbox</value></item><item><key>Deployment</key><value>dev</value></item><item><key>Owner</key><value>joe</value></item><item><key>ExpiresAt</key><value>2000-01-01T00:00:00Z</value></item></tagSet></item></instancesSet></item></reservationSet></DescribeInstancesResponse>`, account, instanceID, state, volumeID)
		case "TerminateInstances":
			if r.Form.Get("InstanceId.1") != instanceID || r.Form.Get("InstanceId.2") != "" {
				t.Error("unsafe termination IDs")
			}
			state = "terminated"
			fmt.Fprintf(w, `<TerminateInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><instancesSet><item><instanceId>%s</instanceId><currentState><name>shutting-down</name></currentState></item></instancesSet></TerminateInstancesResponse>`, instanceID)
		case "DescribeVolumes":
			if r.Form.Get("VolumeId.1") != volumeID || r.Form.Get("VolumeId.2") != "" {
				t.Error("unsafe volume IDs")
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<Response><Errors><Error><Code>InvalidVolume.NotFound</Code><Message>fixture missing</Message></Error></Errors></Response>`)
		default:
			t.Errorf("unexpected AWS operation %s", action)
			http.Error(w, "fixture", 500)
		}
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL", server.URL)
	t.Setenv("AWS_ENDPOINT_URL_STS", server.URL)
	t.Setenv("AWS_ENDPOINT_URL_EC2", server.URL)
	for _, tc := range []struct {
		name                      string
		closedOut, closedErr, dry bool
		wantExit, wantWrites      int
	}{
		{name: "normal", wantWrites: 1},
		{name: "closed-stdout-dry-run", closedOut: true, dry: true, wantExit: 1},
		{name: "closed-stdout-after-cleanup", closedOut: true, wantExit: 1, wantWrites: 1},
		{name: "closed-stderr-denies-cleanup", closedErr: true, wantExit: 1},
		{name: "closed-both-denies-cleanup", closedOut: true, closedErr: true, wantExit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			actions = nil
			state = "running"
			mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			args := []string{"--config", path, "cleanup", "--json", "--timeout", "3s"}
			if tc.dry {
				args = append(args, "--dry-run")
			}
			cmd := exec.CommandContext(ctx, binary, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if tc.closedOut {
				cmd.Stdout = cleanupClosedPipe(t)
			}
			if tc.closedErr {
				cmd.Stderr = cleanupClosedPipe(t)
			}
			err := cmd.Run()
			if ctx.Err() != nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.wantExit {
				t.Fatalf("exit want=%d err=%v state=%v stdout=%s stderr=%s", tc.wantExit, err, cmd.ProcessState, stdout.String(), stderr.String())
			}
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || status.Signaled() {
				t.Fatalf("cleanup died from signal: %v", cmd.ProcessState)
			}
			mu.Lock()
			gotActions := append([]string(nil), actions...)
			mu.Unlock()
			writes := 0
			for _, a := range gotActions {
				if a == "TerminateInstances" {
					writes++
				}
			}
			if writes != tc.wantWrites {
				t.Fatalf("wrong termination count: %v", gotActions)
			}
			if tc.dry && strings.Join(gotActions, ",") != "GetCallerIdentity,DescribeInstances" {
				t.Fatalf("dry-run performed extra work: %v", gotActions)
			}
			if tc.closedOut && tc.closedErr {
				return
			} // Neither destination can retain bytes.
			var result expiry.Result
			if tc.closedOut {
				marker := "devbox: cannot write cleanup result; complete recovery result follows\n"
				_, body, found := strings.Cut(stderr.String(), marker)
				if !found {
					t.Fatalf("missing fallback: %s", stderr.String())
				}
				result = decodeCleanupProcessResult(t, body)
			} else {
				result = decodeCleanupProcessResult(t, stdout.String())
			}
			if result.Scope.Account != account || len(result.Instances) != 1 || result.Instances[0].InstanceID != instanceID || len(result.Instances[0].Volumes) != 1 || result.Instances[0].Volumes[0].ID != volumeID {
				t.Fatalf("lost recovery identities: %+v", result)
			}
			if tc.closedErr {
				if result.OK || result.ExitCode != 1 || result.TerminatedCount != 0 || result.CleanedCount != 0 {
					t.Fatalf("unacknowledged evidence authorized cleanup: %+v", result)
				}
				data, _ := json.Marshal(result)
				if !bytes.Contains(data, []byte("evidence_unavailable")) {
					t.Fatalf("missing evidence failure: %s", data)
				}
			} else if !tc.dry && result.CleanedCount != 1 {
				t.Fatalf("lost completed outcome: %+v", result)
			}
		})
	}
	t.Run("other-command-keeps-worker-sigpipe", func(t *testing.T) {
		// Bypass only the supervisor to observe the worker's native signal status.
		// Cleanup-specific handling must not change e.g. version's process behavior.
		cmd := exec.Command(binary, "__devbox_worker", "version")
		cmd.Stdout = cleanupClosedPipe(t)
		err := cmd.Run()
		if err == nil || cmd.ProcessState == nil {
			t.Fatal("expected SIGPIPE", err)
		}
		status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGPIPE {
			t.Fatalf("other-command pipe behavior changed: %v", cmd.ProcessState)
		}
	})
}

func cleanupClosedPipe(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}
func decodeCleanupProcessResult(t *testing.T, body string) expiry.Result {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(body))
	var result expiry.Result
	if err := d.Decode(&result); err != nil {
		t.Fatalf("invalid result %q: %v", body, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		t.Fatalf("multiple/trailing results: %q", body)
	}
	if result.SchemaVersion != 1 || result.Command != "cleanup" || result.Instances == nil || result.Errors == nil {
		t.Fatalf("invalid envelope: %+v", result)
	}
	return result
}
