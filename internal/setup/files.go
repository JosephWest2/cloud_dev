package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Refuse symlinks in every existing component, not only at the leaf.
func safePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return invalid("setup paths must be absolute, clean paths without control characters")
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail("files_unavailable", "cannot inspect setup paths")
		}
		if err == nil && (st.Mode()&os.ModeSymlink != 0 || (p != path && !st.IsDir())) {
			return fail("unsafe_path", "setup refuses symlink or non-directory parent paths; choose a regular private location")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

func readFile(path string) ([]byte, error) {
	if err := safePath(path); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 16<<20 {
		return nil, fail("files_unavailable", "setup input must be a bounded regular file")
	}
	return os.ReadFile(path)
}

func atomicWrite(path string, body []byte) error {
	if err := safePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fail("files_unavailable", "cannot create private setup directory")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".devbox-write-*")
	if err != nil {
		return fail("files_unavailable", "cannot stage private setup file")
	}
	defer os.Remove(f.Name())
	err = f.Chmod(0600)
	if err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	if err != nil {
		return fail("files_unavailable", "cannot durably publish private setup file")
	}
	return nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func lockDirectory(path string) (func(), error) {
	if err := safePath(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fail("files_unavailable", "cannot create setup workspace")
	}
	lock := filepath.Join(path, "setup.lock")
	if err := safePath(lock); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fail("workspace_locked", "cannot acquire setup workspace lock")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, fail("workspace_locked", "another setup owns this workspace; wait for it to exit")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

type FileChange struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
	Staged string `json:"staged"`
	Backup string `json:"backup"`
}

func fileDigest(path string) (string, error) {
	b, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	return hash(b), nil
}

func stageChanges(workspace string, files map[string][]byte) ([]FileChange, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	changes := []FileChange{}
	for i, p := range paths {
		before, err := fileDigest(p)
		if err != nil {
			return nil, err
		}
		if before == hash(files[p]) {
			continue
		}
		change := FileChange{Path: p, Before: before, After: hash(files[p]), Staged: filepath.Join(workspace, fmt.Sprintf("publish-%d.new", i)), Backup: filepath.Join(workspace, fmt.Sprintf("publish-%d.backup", i))}
		if before != "absent" {
			old, err := readFile(p)
			if err != nil {
				return nil, err
			}
			if err = atomicWrite(change.Backup, old); err != nil {
				return nil, err
			}
		}
		if err := atomicWrite(change.Staged, files[p]); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// All targets are checked before publishing any; a crash resumes only changes
// whose old or new bytes still match the durable transaction.
func publishChanges(changes []FileChange) error {
	for _, c := range changes {
		got, err := fileDigest(c.Path)
		if err != nil {
			return err
		}
		if got != c.Before && got != c.After {
			return fail("files_changed", "a configuration file changed after preview; retain backups and review the concurrent edit")
		}
		staged, err := readFile(c.Staged)
		if err != nil || hash(staged) != c.After {
			return fail("files_changed", "staged configuration changed; recover before publishing")
		}
	}
	for _, c := range changes {
		got, err := fileDigest(c.Path)
		if err != nil {
			return err
		}
		if got == c.After {
			continue
		}
		if got != c.Before {
			return fail("files_changed", "configuration changed during publication")
		}
		b, err := readFile(c.Staged)
		if err != nil {
			return err
		}
		if err = atomicWrite(c.Path, b); err != nil {
			return err
		}
	}
	return nil
}

// Edit only known top-level scalar keys. Existing comments and unrelated fields
// remain byte-for-byte; unsupported complex definitions fail validation later.
func patchTOML(old []byte, values map[string]any) ([]byte, error) {
	lines := strings.Split(string(old), "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		v, selected := values[key]
		if !ok || !selected {
			continue
		}
		if seen[key] {
			return nil, invalid("existing configuration contains duplicate setup fields")
		}
		seen[key] = true
		encoded, _ := json.Marshal(v)
		lines[i] = key + " = " + string(encoded)
	}
	keys := []string{}
	for k := range values {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		b, _ := json.Marshal(values[k])
		lines = append(lines, k+" = "+string(b))
	}
	return []byte(strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"), nil
}

func profileSection(old []byte, name string, fields map[string]string) ([]byte, error) {
	if !profilePattern.MatchString(name) {
		return nil, invalid("choose a simple named AWS profile")
	}
	header := "[profile " + name + "]"
	start, end := -1, -1
	lines := strings.Split(string(old), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			if start >= 0 && end < 0 {
				end = i
			}
			if trim == header {
				if start >= 0 {
					return nil, invalid("AWS configuration contains a duplicate profile section")
				}
				start = i
			}
		}
	}
	if start >= 0 {
		if end < 0 {
			end = len(lines)
		}
		got := map[string]string{}
		for _, line := range lines[start+1 : end] {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return nil, invalid("existing managed AWS profile has unsupported content; select another profile name")
			}
			got[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(fields)
		if string(a) != string(b) {
			return nil, fail("profile_conflict", "the chosen AWS profile already has different settings; choose a new name instead of replacing it")
		}
		return old, nil
	}
	var b strings.Builder
	b.Write(old)
	if len(old) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(header + "\n")
	keys := []string{}
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.ContainsAny(fields[k], "\r\n\x00") {
			return nil, invalid("invalid AWS profile value")
		}
		b.WriteString(k + " = " + fields[k] + "\n")
	}
	return []byte(b.String()), nil
}
