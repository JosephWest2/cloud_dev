//go:build linux

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestInteractiveLoginBoundary(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	var unlock int32
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != 0 {
		t.Fatal(err)
	}
	var number uint32
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); err != 0 {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	original := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = original }()
	dir := t.TempDir()
	path := testutil.Setup(t)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", testutil.Write(t, filepath.Join(dir, "config"), "[profile test]\nlogin_session=test\n"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", testutil.Write(t, filepath.Join(dir, "credentials"), ""))
	t.Setenv("LOGIN_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("PATH", dir)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LOGIN_CALLS\"\nif [ \"$1\" = configure ]; then echo SECRET; echo 'Your session has expired.' >&2; exit 1; fi\necho AUTHENTICATION\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		json    bool
	}{{"doctor", true}, {"proxy", false}, {"ssh-config", false}, {"logs", false}} {
		var output bytes.Buffer
		prepareCommandLogin(context.Background(), path, config.Overrides{}, tc.command, tc.json, &output)
		if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
			t.Fatal("machine command started authentication")
		}
	}
	var output, diagnostics bytes.Buffer
	calls := 0
	deps := doctor.Dependencies{Identity: func(context.Context, config.Config) error {
		calls++
		return &identity.Failure{Code: "account_mismatch", Message: "wrong account"}
	}, Plugin: func(context.Context) error { return nil }, SSH: func(context.Context) error { return nil }, GOOS: "linux"}
	if code := Run(context.Background(), []string{"doctor", "--config", path}, &output, &diagnostics, deps); code != 1 || calls != 1 {
		t.Fatalf("normal identity gate bypassed: %d %d", code, calls)
	}
	body, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "\n") != 2 || !strings.Contains(string(body), "login --profile test") {
		t.Fatalf("unexpected auth calls: %s", body)
	}
	if strings.Contains(output.String(), "AUTHENTICATION") || strings.Contains(output.String()+diagnostics.String(), "SECRET") {
		t.Fatal("subprocess output leaked to command output")
	}
	if !strings.Contains(diagnostics.String(), "AUTHENTICATION") {
		t.Fatal("login output not attached to terminal diagnostics")
	}
}
