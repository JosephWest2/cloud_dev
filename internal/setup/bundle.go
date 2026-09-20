package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Bundle struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	SourceCommit  string            `json:"source_commit"`
	Files         map[string]string `json:"files"`
}

func hashFile(path string) (string, error) {
	if err := safePath(path); err != nil {
		return "", err
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 512<<20 {
		return "", fail("bundle_invalid", "bundle file is missing or invalid")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateBundle(dir, version string) (Bundle, string, error) {
	var b Bundle
	raw, err := readFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return b, "", fail("bundle_unavailable", "install the matching release setup bundle or pass --bundle PATH")
	}
	if err = strictJSON(raw, &b); err != nil {
		return b, "", err
	}
	if b.SchemaVersion != 1 || b.Version != version || len(b.SourceCommit) != 40 || len(b.Files) < 10 {
		return b, "", fail("bundle_mismatch", "CLI and setup bundle must be from the same release; resume with the recorded release")
	}
	for _, name := range []string{"infra/state-bootstrap/main.tf", "infra/state-bootstrap/backend.tf.example", "infra/state-bootstrap/.terraform.lock.hcl", "infra/foundation/main.tf", "infra/foundation/versions.tf", "infra/foundation/.terraform.lock.hcl", "infra/foundation/bootstrap.sh", "bin/devbox-runner-linux-amd64", "bin/devbox-cleanup-linux-amd64.zip"} {
		if !digestPattern.MatchString(b.Files[name]) {
			return b, "", fail("bundle_invalid", "setup bundle is incomplete")
		}
	}
	for name, want := range b.Files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "../") || !digestPattern.MatchString(want) {
			return b, "", fail("bundle_invalid", "unsafe bundle file entry")
		}
		got, err := hashFile(filepath.Join(dir, name))
		if err != nil || got != want {
			return b, "", fail("bundle_changed", "setup bundle bytes do not match its manifest")
		}
	}
	// Unmanifested Terraform or executable files must not influence a plan.
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fail("bundle_invalid", "bundle symlinks are not supported")
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel != "bundle.json" && b.Files[rel] == "" {
			return fail("bundle_invalid", "bundle contains unverified files")
		}
		return nil
	})
	return b, hash(raw), err
}

func copyBundle(src, dst string, b Bundle) error {
	for name, want := range b.Files {
		path := filepath.Join(dst, name)
		if err := safePath(path); err != nil {
			return err
		}
		if got, err := hashFile(path); err == nil {
			if got != want {
				return fail("workspace_changed", "setup workspace differs from its pinned bundle")
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		in, err := os.Open(filepath.Join(src, name))
		if err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			in.Close()
			return fail("workspace_changed", "cannot create a pinned workspace file")
		}
		_, err = io.Copy(out, in)
		in.Close()
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
