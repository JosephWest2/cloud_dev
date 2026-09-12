package access

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func fixture(t *testing.T) (config.Config, lifecycle.Instance, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := testutil.Setup(t)
	c, e := config.Load(path, config.Overrides{})
	if e != nil {
		t.Fatal(e)
	}
	c.SSHIdentityFile = filepath.Join(t.TempDir(), "id with 'quote % key")
	return c, lifecycle.Instance{ID: "i-12345678", HostKey: testKey}, path
}
func TestHostTrustAndSSHConfig(t *testing.T) {
	c, i, path := fixture(t)
	a, err := artifacts(context.Background(), c, i, path, "/tmp/devbox 'bin %x")
	if err != nil {
		t.Fatal(err)
	}
	// Let the actual OpenSSH parser inspect the generated stanza without opening
	// a network connection. -G confirms token quoting and the trust restrictions.
	out, err := exec.Command("ssh", "-G", "-F", a.ConfigPath, a.Alias).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"user devbox", "stricthostkeychecking true", "identitiesonly yes", "forwardagent no", "hostkeyalgorithms ssh-ed25519", "globalknownhostsfile /dev/null", "updatehostkeys false"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if !strings.Contains(a.Config, "%%x") || !strings.Contains(a.Config, "'\"'\"'") {
		t.Fatal("missing proxy escaping")
	}
	b, _ := base64.StdEncoding.DecodeString(strings.Fields(testKey)[1])
	b[len(b)-1] = 1
	i.HostKey = "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
	if _, err = artifacts(context.Background(), c, i, path, "/bin/devbox"); err == nil {
		t.Fatal("host change accepted")
	}
	saved, _ := os.ReadFile(a.KnownHosts)
	if !bytes.Contains(saved, []byte(testKey)) {
		t.Fatal("replaced trust on mismatch")
	}
}
func TestConcurrentHostTrustAndUnsafePaths(t *testing.T) {
	c, i, path := fixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := artifacts(context.Background(), c, i, path, "/bin/devbox")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.SSHIdentityFile = "/tmp/key\nProxyCommand malicious"
	if _, err := artifacts(context.Background(), c, i, path, "/bin/devbox"); err == nil {
		t.Fatal("config injection accepted")
	}
	c.SSHIdentityFile = "/tmp/${SECRET}"
	if _, err := artifacts(context.Background(), c, i, path, "/bin/devbox"); err == nil {
		t.Fatal("environment expansion accepted")
	}
}
func TestStartupAdapterRedactsAndPreservesBinary(t *testing.T) {
	payload := []byte("SSH-2.0-Test\r\n\x00\xff\x01\nSECRET_BINARY")
	input := append([]byte("\nStarting session with SessionId: example\nSECRET_STARTUP\n"), payload...)
	for _, size := range []int{1, 7, 4096} {
		var out bytes.Buffer
		err := copySSH(&out, &chunkReader{b: input, n: size})
		if err != nil || !bytes.Equal(out.Bytes(), payload) {
			t.Fatalf("size=%d %v %q", size, err, out.Bytes())
		}
	}
	for _, input := range []string{"Cannot perform start session: SECRET\n", strings.Repeat("x", 20000)} {
		var out bytes.Buffer
		if err := copySSH(&out, strings.NewReader(input)); err == nil || out.Len() != 0 {
			t.Fatal("startup error leaked")
		}
	}
}

type chunkReader struct {
	b []byte
	n int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := min(len(r.b), len(p), r.n)
	copy(p, r.b[:n])
	r.b = r.b[n:]
	if len(r.b) == 0 {
		return n, nil
	}
	return n, nil
}
func TestPluginPrerequisiteAndChildEnvironment(t *testing.T) {
	for s, want := range map[string]bool{"1.2.835.0": true, "1.2.764.0": true, "1.2.763.9": false, "2.0.0.0": true, "1.3.SECRET.1": false, "SECRET": false} {
		if got := pluginVersionOK(s); got != want {
			t.Fatalf("%s %t", s, got)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "seelog.xml")
	if !loggingDisabled(path) {
		t.Fatal("absent default rejected")
	}
	for body, want := range map[string]bool{`<seelog minlevel="off"/>`: true, `<seelog minlevel="debug"/>`: false, `<seelog minlevel="off"><exceptions><exception minlevel="debug"/></exceptions></seelog>`: false} {
		testutil.Write(t, path, body)
		if loggingDisabled(path) != want {
			t.Fatal(body)
		}
	}
	t.Setenv(responseEnv, "OLD_SECRET")
	env := childEnv([]byte("NEW_SECRET"))
	n := 0
	for _, s := range env {
		if strings.HasPrefix(s, responseEnv+"=") {
			n++
			if s != responseEnv+"=NEW_SECRET" {
				t.Fatal("old token")
			}
		}
	}
	if n != 1 || os.Getenv(responseEnv) != "OLD_SECRET" {
		t.Fatal("mutated parent environment")
	}
}
func TestSetupDeadlineDoesNotBoundEstablishedShell(t *testing.T) {
	c, i, path := fixture(t)
	a, err := artifacts(context.Background(), c, i, path, "/bin/false")
	if err != nil {
		t.Fatal(err)
	}
	// A controlled ssh executable models the master becoming authenticated, then
	// a shell that outlives setup. It never reaches AWS or a real network.
	dir := t.TempDir()
	script := `#!/bin/sh
case " $* " in
 *" -O check "*) exit 0;;
 *" -O exit "*) exit 0;;
 *" -M "*) sleep 5;;
 *) sleep .08; exit 7;;
esac
`
	ssh := testutil.Write(t, filepath.Join(dir, "ssh"), script)
	os.Chmod(ssh, 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	setup, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if code := interactive(context.Background(), setup, a, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 7 {
		t.Fatalf("session killed by setup deadline: %d", code)
	}
}
