package access

import (
	"context"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/sshkey"
)

const pluginLogConfig = "/usr/local/sessionmanagerplugin/seelog.xml"

func loggingDisabled(path string) bool {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true
	} // Supported upstream default has minlevel=off.
	if err != nil || len(b) > 64*1024 {
		return false
	}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	root := false
	for {
		token, err := d.Token()
		if err == io.EOF {
			return root
		}
		if err != nil {
			return false
		}
		if el, ok := token.(xml.StartElement); ok {
			if !root {
				if el.Name.Local != "seelog" {
					return false
				}
				off := false
				for _, a := range el.Attr {
					if a.Name.Local == "minlevel" && a.Value == "off" {
						off = true
					}
				}
				if !off {
					return false
				}
				root = true
			} else if el.Name.Local == "exception" {
				return false
			}
		}
	}
}
func pluginVersionOK(s string) bool {
	fields := strings.Split(strings.TrimSpace(s), ".")
	if len(fields) != 4 {
		return false
	}
	want := []int{1, 2, 764, 0}
	for n, f := range fields {
		v, err := strconv.Atoi(f)
		if err != nil || v < 0 {
			return false
		}
		if v != want[n] { // Validate remaining fields even on a greater prefix.
			for _, r := range fields[n+1:] {
				if x, e := strconv.Atoi(r); e != nil || x < 0 {
					return false
				}
			}
			return v > want[n]
		}
	}
	return true
}
func CheckPlugin(ctx context.Context) (string, error) {
	if !loggingDisabled(pluginLogConfig) {
		return "", fail("plugin_logging_enabled", "disable Session Manager plugin logging: remove or set /usr/local/sessionmanagerplugin/seelog.xml to minlevel=off without exceptions; retry after configuration is stable")
	}
	path, err := exec.LookPath("session-manager-plugin")
	if err != nil {
		return "", fail("plugin_unavailable", "install the AWS Session Manager plugin and verify session-manager-plugin --version")
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out boundedBuffer
	cmd := exec.CommandContext(probe, path, "--version")
	cmd.Stdout = &out
	if err = cmd.Run(); err != nil || !pluginVersionOK(out.String()) {
		if probe.Err() != nil {
			return "", probe.Err()
		}
		return "", fail("plugin_version_unsupported", "Session Manager plugin must report version 1.2.764.0 or newer; reinstall a supported official release")
	}
	return path, nil
}
func prerequisites(ctx context.Context, c config.Config, m config.Manifest) error {
	if runtime.GOOS != "linux" {
		return fail("platform_unsupported", "SSH access currently supports Linux only")
	}
	if err := doctor.CheckSSH(ctx); err != nil {
		return fail("ssh_unavailable", "install OpenSSH and verify ssh -V")
	}
	if _, err := CheckPlugin(ctx); err != nil {
		return err
	}
	if c.SSHIdentityFile == "" {
		return fail("ssh_identity_missing", "set ssh_identity_file in devbox TOML to your dedicated Ed25519 private-key path; keep the matching .pub file beside it; load encrypted keys with ssh-add")
	}
	if err := privateFile(c.SSHIdentityFile); err != nil {
		return fail("ssh_identity_unavailable", "dedicated SSH identity must be an owned regular private-key file with mode 0600; check ssh_identity_file")
	}
	b, err := os.ReadFile(c.SSHIdentityFile + ".pub")
	if err != nil || len(b) > 4096 {
		return fail("ssh_public_key_unavailable", "keep the dedicated identity's .pub file beside it and match the public key configured in the foundation")
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return fail("ssh_public_key_invalid", "dedicated identity .pub must contain an Ed25519 public key")
	}
	key := strings.Join(fields[:2], " ")
	if _, err = sshkey.Parse(key); err != nil || key != m.SSHPublicKey {
		return fail("ssh_key_mismatch", "local public key differs from the foundation export; select the matching identity or rotate the foundation and launch a new worker")
	}
	return nil
}

// Limit even untrusted local process output while allowing the child to finish.
type boundedBuffer struct{ b []byte }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.b) < 4096 {
		b.b = append(b.b, p[:min(len(p), 4096-len(b.b))]...)
	}
	return n, nil
}
func (b *boundedBuffer) String() string { return string(b.b) }
