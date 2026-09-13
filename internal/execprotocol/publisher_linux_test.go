package execprotocol

// This executable prototype models an independent remote worker and an observing
// client. It intentionally uses local files instead of S3; finalRecord is a test
// fixture, not the complete production result schema in docs/contracts.md.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

type fakeStore struct{ dir, fail string }
type streamRecord struct {
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}
type finalRecord struct {
	SchemaVersion int          `json:"schema_version"`
	CommandID     string       `json:"command_id"`
	Complete      bool         `json:"complete"`
	ExitCode      int          `json:"exit_code"`
	Stdout        streamRecord `json:"stdout"`
	Stderr        streamRecord `json:"stderr"`
}

// Publish atomically links a fully written object into its create-once key.
// Failure injection models an upload error; it is not an S3 consistency model.
func (s fakeStore) put(key string, b []byte) error {
	if s.fail == key {
		return errors.New("injected storage denial")
	}
	f, err := os.CreateTemp(s.dir, ".upload-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), filepath.Join(s.dir, key))
}

func streamMeta(b []byte) streamRecord {
	sum := sha256.Sum256(b)
	return streamRecord{len(b), hex.EncodeToString(sum[:])}
}

func prototypePublish(s fakeStore, stdout, stderr []byte, exit int) error {
	if s.fail == "partial" {
		if err := s.put("stdout", stdout[:len(stdout)/2]); err != nil {
			return err
		}
		return errors.New("injected interrupted upload")
	}
	if err := s.put("stdout", stdout); err != nil {
		return err
	}
	if err := s.put("stderr", stderr); err != nil {
		return err
	}
	// Final is the last object: failed stream publication cannot claim complete.
	b, _ := json.Marshal(finalRecord{1, "prototype-command", true, exit, streamMeta(stdout), streamMeta(stderr)})
	return s.put("final.json", b)
}

func waitFile(path string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return errors.New("prototype synchronization timed out")
}

func fixtureOutput(args []string, empty bool) ([]byte, []byte) {
	if empty {
		return nil, nil
	}
	argv, _ := json.Marshal(args)
	return append(append(argv, '\n'), bytes.Repeat([]byte{0, 0xff, 'o'}, 20000)...), bytes.Repeat([]byte{0xfe, 0, 'e'}, 10000)
}

// TestPrototypeProcess is re-entered in child processes, never in production.
func TestPrototypeProcess(t *testing.T) {
	role := os.Getenv("DEVBOX_PROTOTYPE_ROLE")
	if role == "" {
		return
	}
	store := fakeStore{dir: os.Getenv("DEVBOX_PROTOTYPE_STORE")}
	control := os.Getenv("DEVBOX_PROTOTYPE_CONTROL")
	exit := 0
	err := func() error {
		switch role {
		case "observer":
			if err := store.put("initial.json", []byte(os.Getenv("DEVBOX_PROTOTYPE_PAYLOAD"))); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(control, "ack"), []byte("prototype-command"), 0600); err != nil {
				return err
			}
			for {
				time.Sleep(time.Hour)
			}
		case "worker":
			if err := waitFile(filepath.Join(control, "start")); err != nil {
				return err
			}
			b, err := os.ReadFile(filepath.Join(store.dir, "initial.json"))
			if err != nil {
				return err
			}
			p, err := DecodePayload(string(b))
			if err != nil {
				return err
			}
			cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
			cmd.Dir = p.Cwd
			cmd.Env = append(os.Environ(), "DEVBOX_PROTOTYPE_ROLE=fixture")
			var out, diag bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diag
			err = cmd.Run() // nil stdin supplies EOF, no implicit shell.
			if err != nil {
				var status *exec.ExitError
				if !errors.As(err, &status) || status.ExitCode() < 0 {
					return err
				}
				exit = status.ExitCode()
			}
			return prototypePublish(store, out.Bytes(), diag.Bytes(), exit)
		case "fixture":
			if err := os.WriteFile(filepath.Join(control, "running"), nil, 0600); err != nil {
				return err
			}
			if err := waitFile(filepath.Join(control, "finish")); err != nil {
				return err
			}
			var args []string
			for n, a := range os.Args {
				if a == "--" {
					args = os.Args[n+1:]
					break
				}
			}
			out, diag := fixtureOutput(args, os.Getenv("DEVBOX_PROTOTYPE_EMPTY") == "true")
			if _, err := os.Stdout.Write(out); err != nil {
				return err
			}
			if _, err := os.Stderr.Write(diag); err != nil {
				return err
			}
			exit, _ = strconv.Atoi(os.Getenv("DEVBOX_PROTOTYPE_EXIT"))
			return nil
		}
		return errors.New("unknown prototype role")
	}()
	if err != nil {
		os.Exit(99)
	}
	if role == "worker" {
		os.Exit(0)
	}
	os.Exit(exit)
}

func TestPrototypeObserverDisappearance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during bool
		exit   int
		empty  bool
	}{
		{"ack-success", false, 0, false}, {"during-exit1", true, 1, false},
		{"ack-exit2", false, 2, false}, {"during-exit4", true, 4, false},
		{"ack-exit255", false, 255, false}, {"during-empty-success", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects, control := t.TempDir(), t.TempDir()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			argv := append([]string{binary, "-test.run=^TestPrototypeProcess$", "--"}, literalArgs...)
			payload, _ := NewPayload(argv, t.TempDir(), 30)
			encoded, err := EncodePayload(payload)
			if err != nil {
				t.Fatal(err)
			}
			env := append(os.Environ(), "DEVBOX_PROTOTYPE_STORE="+objects, "DEVBOX_PROTOTYPE_CONTROL="+control, "DEVBOX_PROTOTYPE_PAYLOAD="+encoded, "DEVBOX_PROTOTYPE_EXIT="+strconv.Itoa(tc.exit), "DEVBOX_PROTOTYPE_EMPTY="+strconv.FormatBool(tc.empty))
			start := func(role string) *exec.Cmd {
				cmd := exec.Command(binary, "-test.run=^TestPrototypeProcess$")
				cmd.Env = append(env, "DEVBOX_PROTOTYPE_ROLE="+role)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
				return cmd
			}
			worker, observer := start("worker"), start("observer")
			wait := func(key string) {
				t.Helper()
				if err := waitFile(filepath.Join(control, key)); err != nil {
					t.Fatal(err)
				}
			}
			release := func(key string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(control, key), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			wait("ack")
			if tc.during {
				release("start")
				wait("running")
			}
			if err := observer.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := observer.Wait(); err == nil {
				t.Fatal("observer should have died")
			}
			if _, err := os.Stat(filepath.Join(objects, "final.json")); !os.IsNotExist(err) {
				t.Fatal("worker finalized before execution was released")
			}
			if !tc.during {
				release("start")
				wait("running")
			}
			release("finish")
			if err := worker.Wait(); err != nil {
				t.Fatal("independent worker:", err)
			}
			// Forget payload, client acknowledgement and execution control. Recovery
			// now uses only the public key in the fake remote object store.
			if err := os.RemoveAll(control); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(objects, "final.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record finalRecord
			if err := json.Unmarshal(b, &record); err != nil {
				t.Fatal(err)
			}
			wantOut, wantErr := fixtureOutput(literalArgs, tc.empty)
			if record.SchemaVersion != 1 || record.CommandID != "prototype-command" || !record.Complete || record.ExitCode != tc.exit || record.Stdout != streamMeta(wantOut) || record.Stderr != streamMeta(wantErr) {
				t.Fatalf("wrong complete record: %+v", record)
			}
			for key, want := range map[string][]byte{"stdout": wantOut, "stderr": wantErr} {
				got, err := os.ReadFile(filepath.Join(objects, key))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("incomplete %s: %v", key, err)
				}
			}
		})
	}
}

func TestPrototypePublicationFailureAndImmutability(t *testing.T) {
	for _, fail := range []string{"stdout", "stderr", "final.json", "partial"} {
		s := fakeStore{t.TempDir(), fail}
		if err := prototypePublish(s, []byte("output"), []byte("diagnostic"), 2); err == nil {
			t.Fatal("injected failure succeeded")
		}
		if _, err := os.Stat(filepath.Join(s.dir, "final.json")); !os.IsNotExist(err) {
			t.Fatal("failed publication claimed complete")
		}
	}
	s := fakeStore{dir: t.TempDir()}
	if err := s.put("object", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := s.put("object", []byte("overwrite")); !os.IsExist(err) {
		t.Fatalf("create-once error: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "object"))
	if err != nil || string(b) != "original" {
		t.Fatal("existing object changed")
	}
}
