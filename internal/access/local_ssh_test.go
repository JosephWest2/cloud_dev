package access

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

// Exercise an actual OpenSSH master and multiplexed shell over an inetd stdio
// server. No listening port, AWS calls, existing keys or authorized_keys changes.
func localSSHConfig(t *testing.T) string {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("local inetd sshd fixture unavailable")
	}
	who, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	clientKey := filepath.Join(dir, "client")
	hostKey := filepath.Join(dir, "host")
	for _, key := range []string{clientKey, hostKey} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
			t.Fatalf("test key generation: %v %s", err, out)
		}
	}
	server := testutil.Write(t, filepath.Join(dir, "server_config"), "HostKey "+hostKey+"\nAuthorizedKeysFile "+clientKey+".pub\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nPermitRootLogin yes\nAllowUsers "+who.Username+"\nForceCommand /bin/sh\nPermitUserRC no\nLogLevel ERROR\n")
	if out, err := exec.Command(sshd, "-t", "-f", server).CombinedOutput(); err != nil {
		t.Skipf("local sshd fixture unsupported: %s", out)
	}
	host, _ := os.ReadFile(hostKey + ".pub")
	known := testutil.Write(t, filepath.Join(dir, "known_hosts"), "local-test "+strings.Join(strings.Fields(string(host))[:2], " ")+"\n")
	cfg := testutil.Write(t, filepath.Join(dir, "client_config"), "Host local-test\n HostName local-test\n User "+who.Username+"\n IdentityFile "+clientKey+"\n IdentitiesOnly yes\n BatchMode yes\n StrictHostKeyChecking yes\n UserKnownHostsFile "+known+"\n GlobalKnownHostsFile /dev/null\n ProxyCommand "+ShellQuote(sshd)+" -i -f "+ShellQuote(server)+"\n")
	return cfg
}

func TestLocalSSHMasterPreservesRemoteExitStatus(t *testing.T) {
	cfg := localSSHConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	setup, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var out, diag bytes.Buffer
	code, err := interactive(ctx, setup, Artifacts{Alias: "local-test", ConfigPath: cfg}, strings.NewReader("printf LOCAL_SSH_OK\\n\nexit 4\n"), &out, &diag)
	if code != 4 || err != nil || !strings.Contains(out.String(), "LOCAL_SSH_OK") {
		t.Fatalf("real SSH result code=%d err=%v output=%q diagnostic=%q", code, err, out.String(), diag.String())
	}
}
