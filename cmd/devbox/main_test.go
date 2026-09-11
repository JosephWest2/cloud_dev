//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

// Exercise the real executable boundary: SDK credential_process bypasses the
// CLI's stderr writer and would otherwise expose helper output on os.Stderr.
func TestCredentialProcessCannotLeakDiagnostics(t *testing.T) {
	testutil.IsolateAWS(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "devbox")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	t.Setenv("AWS_CONFIG_FILE", testutil.Write(t, filepath.Join(dir, "aws-config"), "[profile test]\ncredential_process = sh -c 'echo SECRET >&2; echo SECRET; exit 1'\n"))
	plugin := testutil.Write(t, filepath.Join(dir, "session-manager-plugin"), "#!/bin/sh\necho PLUGIN_SECRET >&2\necho PLUGIN_SECRET\nexit 0\n")
	if err := os.Chmod(plugin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command(binary, "doctor", "--config", testutil.Setup(t), "--json", "--timeout", "2s")
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	err := cmd.Run()
	if err == nil {
		t.Fatal("failed credentials must fail doctor")
	}
	if !json.Valid(out.Bytes()) || strings.Contains(out.String()+diag.String(), "SECRET") {
		t.Fatalf("unsafe output: %s %s", &out, &diag)
	}
}

func TestTimedOutCredentialProcessAndDescendantAreStopped(t *testing.T) {
	testutil.IsolateAWS(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "devbox")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	helperPID, childPID := filepath.Join(dir, "helper.pid"), filepath.Join(dir, "child.pid")
	t.Cleanup(func() {
		for _, path := range []string{helperPID, childPID} {
			if data, err := os.ReadFile(path); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
	helper := testutil.Write(t, filepath.Join(dir, "helper"), "#!/bin/sh\necho $$ > '"+helperPID+"'\nsleep 30 &\necho $! > '"+childPID+"'\nwait\n")
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", testutil.Write(t, filepath.Join(dir, "aws-config"), "[profile test]\ncredential_process = exec "+helper+"\n"))
	cmd := exec.Command(binary, "doctor", "--config", testutil.Setup(t), "--json", "--timeout", "500ms")
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 4 || !json.Valid(out.Bytes()) {
		t.Fatalf("timeout result: %v %s %s", err, &out, &diag)
	}
	for _, path := range []string{helperPID, childPID} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for {
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			// A killed orphan may briefly remain a zombie pending init's reaper.
			if os.IsNotExist(err) || (err == nil && strings.Contains(string(stat), ") Z ")) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("credential helper process %d remains running after doctor exit", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
