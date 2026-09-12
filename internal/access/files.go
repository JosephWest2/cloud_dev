// Package access implements the local OpenSSH contract over a scoped SSM tunnel.
package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/sshkey"
)

func fail(code, message string) error { return &lifecycle.Failure{Code: code, Message: message} }

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("insecure directory")
	}
	return nil
}
func privateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("insecure file")
	}
	return nil
}
func atomicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".access-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func lock(ctx context.Context, path string) (func(), error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = privateFile(path); err != nil {
		f.Close()
		return nil, err
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(fd, syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ShellQuote is applied to every argument before OpenSSH expands percent tokens
// and invokes ProxyCommand through the user's shell.
func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func configQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00$") {
		return "", fail("ssh_path_invalid", "SSH configuration paths must not contain control characters or dollar signs")
	}
	s = strings.ReplaceAll(s, "%", "%%")
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

type Artifacts struct{ Alias, Directory, KnownHosts, ConfigPath, Config string }

func artifacts(ctx context.Context, c config.Config, i lifecycle.Instance, configPath, executable string) (Artifacts, error) {
	var a Artifacts
	if _, err := sshkey.Parse(i.HostKey); err != nil {
		return a, fail("host_key_invalid", "no validated host key; retry readiness observation")
	}
	store, err := lifecycle.DefaultStore()
	if err != nil {
		return a, err
	}
	root := filepath.Join(filepath.Dir(store.Dir), "ssh")
	if err = privateDir(root); err != nil {
		return a, fail("ssh_state_unavailable", "SSH state directory must be owned by you with mode 0700; check XDG_STATE_HOME/devbox/ssh")
	}
	digest := sha256.Sum256([]byte(c.ExpectedAccount + "/" + c.Region + "/" + c.Deployment + "/" + c.Owner + "/" + i.ID))
	a.Alias = "devbox-" + i.ID + "-" + hex.EncodeToString(digest[:8])
	a.Directory = filepath.Join(root, a.Alias)
	if err = privateDir(a.Directory); err != nil {
		return a, fail("ssh_state_unavailable", "cannot create private per-instance SSH state directory")
	}
	a.KnownHosts = filepath.Join(a.Directory, "known_hosts")
	a.ConfigPath = filepath.Join(a.Directory, "config")
	unlock, err := lock(ctx, filepath.Join(a.Directory, "lock"))
	if err != nil {
		return a, fail("ssh_state_unavailable", "cannot lock SSH host trust files; check directory permissions and retry")
	}
	defer unlock()
	expected := []byte(a.Alias + " " + i.HostKey + "\n")
	if err = privateFile(a.KnownHosts); err == nil {
		b, e := os.ReadFile(a.KnownHosts)
		if e != nil {
			return a, fail("host_trust_unavailable", "cannot read the dedicated known_hosts file")
		}
		if string(b) != string(expected) {
			return a, fail("host_key_changed", "instance host key differs from its saved trust entry; inspect the instance and verified SSM probe before deliberately removing this instance's known_hosts file; never disable strict checking")
		}
	} else if !os.IsNotExist(err) {
		return a, fail("host_trust_unavailable", "dedicated known_hosts must be an owned regular file with mode 0600")
	} else if err = atomicFile(a.KnownHosts, expected); err != nil {
		return a, fail("host_trust_unavailable", "cannot write the dedicated known_hosts file")
	}
	cp, err := filepath.Abs(configPath)
	if err != nil {
		return a, err
	}
	args := []string{executable, "proxy", i.ID, "--config", cp, "--region", c.Region}
	if c.AWSProfile != "" {
		args = append(args, "--aws-profile", c.AWSProfile)
	}
	quoted := make([]string, len(args))
	for n, s := range args {
		if strings.ContainsAny(s, "\r\n\x00") {
			return a, fail("ssh_path_invalid", "proxy arguments cannot contain control characters")
		}
		quoted[n] = strings.ReplaceAll(ShellQuote(s), "%", "%%")
	}
	identity, err := configQuote(c.SSHIdentityFile)
	if err != nil {
		return a, err
	}
	known, err := configQuote(a.KnownHosts)
	if err != nil {
		return a, err
	}
	a.Config = fmt.Sprintf(`Host %s
    HostName %s
    User devbox
    Port 22
    IdentityFile %s
    IdentitiesOnly yes
    BatchMode yes
    ForwardAgent no
    StrictHostKeyChecking yes
    HostKeyAlias %s
    HostKeyAlgorithms ssh-ed25519
    UserKnownHostsFile %s
    GlobalKnownHostsFile /dev/null
    UpdateHostKeys no
    CheckHostIP no
    ConnectTimeout 300
    ServerAliveInterval 30
    ServerAliveCountMax 3
    ProxyCommand %s
`, a.Alias, i.ID, identity, a.Alias, known, strings.Join(quoted, " "))
	if err = atomicFile(a.ConfigPath, []byte(a.Config)); err != nil {
		return a, fail("ssh_config_unavailable", "cannot save generated SSH configuration")
	}
	return a, nil
}
