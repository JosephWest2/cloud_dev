package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

const fixtureMode = "__runner_test_fixture"

type processObservation struct {
	Args, Env  []string
	Cwd        string
	UID        int
	StdinBytes int
	Mode       uint32
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == WorkloadMode {
		os.Exit(WorkloadMain())
	}
	if len(os.Args) > 2 && os.Args[1] == fixtureMode {
		os.Exit(processFixture(os.Args[2:]))
	}
	os.Exit(m.Run())
}
func processFixture(args []string) int {
	switch args[0] {
	case "inspect":
		cwd, _ := os.Getwd()
		stdin, _ := io.ReadAll(os.Stdin)
		f, err := os.OpenFile("umask-observation", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
		if err != nil {
			return 90
		}
		_ = f.Close()
		st, _ := os.Stat("umask-observation")
		_ = json.NewEncoder(os.Stdout).Encode(processObservation{args[1:], os.Environ(), cwd, os.Getuid(), len(stdin), uint32(st.Mode().Perm())})
	case "output":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{0, 0xff, 'o'}, 30000))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte{0, 0xfe, 'e'}, 20000))
		n, _ := strconv.Atoi(args[1])
		return n
	case "empty":
		return 0
	case "descendants", "detached":
		binary, _ := os.Executable()
		child := exec.Command(binary, fixtureMode, "sleep")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if args[0] == "detached" {
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if child.Start() != nil {
			return 91
		}
		_, _ = io.WriteString(os.Stdout, strconv.Itoa(child.Process.Pid)+"\n")
		if args[0] == "detached" {
			return 0
		}
		time.Sleep(30 * time.Second)
	case "sleep":
		time.Sleep(30 * time.Second)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	return 0
}
func executeFixture(t *testing.T, args []string, alter func(*Executor, *execprotocol.Payload)) Captured {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := execprotocol.NewPayload(append([]string{binary, fixtureMode}, args...), t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	e := Executor{HelperPath: binary, Directory: t.TempDir(), StopGrace: 50 * time.Millisecond, Credential: &syscall.Credential{Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid()), NoSetGroups: true}}
	if alter != nil {
		alter(&e, &p)
	}
	prepare, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := e.Execute(context.Background(), prepare, p)
	t.Cleanup(r.Remove)
	return r
}
func TestProcessLiteralEnvironmentCwdAndStdin(t *testing.T) {
	args := []string{"", "two words", "line\nfeed", "'\"", "$(touch injected); & | > *", "こんにちは🙂", "--json", "--", "-leading"}
	r := executeFixture(t, append([]string{"inspect"}, args...), nil)
	if r.Workload.Status != "exited" || r.Workload.ExitCode == nil || *r.Workload.ExitCode != 0 || r.Capture != "complete" {
		t.Fatalf("result %+v", r)
	}
	b, err := os.ReadFile(r.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	var got processObservation
	if json.Unmarshal(b, &got) != nil {
		t.Fatalf("invalid output %q", b)
	}
	if !reflect.DeepEqual(got.Args, args) || !reflect.DeepEqual(got.Env, Environment) || got.UID != os.Getuid() || got.StdinBytes != 0 || got.Mode != 0644 {
		t.Fatalf("unexpected process contract: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(got.Cwd, "umask-observation")); err != nil {
		t.Fatal("cwd not selected:", err)
	}
}
func TestProcessExitOutputAndStartupFailures(t *testing.T) {
	for _, exit := range []int{0, 1, 2, 4, 255} {
		t.Run(strconv.Itoa(exit), func(t *testing.T) {
			r := executeFixture(t, []string{"output", strconv.Itoa(exit)}, nil)
			if r.Workload.Status != "exited" || r.Workload.ExitCode == nil || *r.Workload.ExitCode != exit || r.Capture != "complete" {
				t.Fatalf("result %+v", r)
			}
			for name, want := range map[string][]byte{r.Stdout: bytes.Repeat([]byte{0, 0xff, 'o'}, 30000), r.Stderr: bytes.Repeat([]byte{0, 0xfe, 'e'}, 20000)} {
				got, err := os.ReadFile(name)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("binary capture differs", err)
				}
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*Executor, *execprotocol.Payload)
		status string
		exit   int
	}{
		{"empty", nil, "exited", 0},
		{"missing-path", func(_ *Executor, p *execprotocol.Payload) { p.Argv = []string{"devbox-nonexistent-unique-executable"} }, "exited", 127},
		{"missing-relative", func(_ *Executor, p *execprotocol.Payload) { p.Argv = []string{"./absent"} }, "exited", 127},
		{"bad-format", func(_ *Executor, p *execprotocol.Payload) {
			file := filepath.Join(p.Cwd, "bad-format")
			_ = os.WriteFile(file, []byte("not a binary or shebang"), 0700)
			p.Argv = []string{"./bad-format"}
		}, "exited", 126},
		{"permission", func(_ *Executor, p *execprotocol.Payload) {
			file := filepath.Join(p.Cwd, "blocked")
			_ = os.WriteFile(file, nil, 0600)
			p.Argv = []string{"./blocked"}
		}, "exited", 126},
		{"cwd", func(_ *Executor, p *execprotocol.Payload) { p.Cwd = filepath.Join(p.Cwd, "absent") }, "runner_setup_failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := executeFixture(t, []string{"empty"}, tc.change)
			if r.Workload.Status != tc.status || tc.status == "exited" && (r.Workload.ExitCode == nil || *r.Workload.ExitCode != tc.exit) {
				t.Fatalf("result %+v", r)
			}
		})
	}
}
func TestProcessTimeoutDescendantsAndCaptureBounds(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		r := executeFixture(t, []string{"descendants"}, func(_ *Executor, p *execprotocol.Payload) { p.ExecTimeoutSeconds = 1 })
		if r.Workload.Status != "execution_timeout" || r.Workload.ExitCode != nil || time.Since(start) > 3*time.Second {
			t.Fatalf("unbounded timeout %+v", r)
		}
		checkDescendantStopped(t, r.Stdout)
	})
	t.Run("capture-limit", func(t *testing.T) {
		r := executeFixture(t, []string{"output", "0"}, func(e *Executor, _ *execprotocol.Payload) { e.MaxBytes = 1024 })
		if r.Capture != "incomplete" {
			t.Fatal("overflow claimed complete")
		}
		for _, name := range []string{r.Stdout, r.Stderr} {
			st, err := os.Stat(name)
			if err != nil || st.Size() > 1024 {
				t.Fatal("capture limit failed", err)
			}
		}
	})
	t.Run("detached-pipe", func(t *testing.T) {
		r := executeFixture(t, []string{"detached"}, nil)
		b, _ := os.ReadFile(r.Stdout)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 {
			defer syscall.Kill(pid, syscall.SIGKILL)
		}
		if r.Capture != "incomplete" {
			t.Fatal("escaped pipe holder claimed complete")
		}
	})
	t.Run("signal", func(t *testing.T) {
		r := executeFixture(t, []string{"signal"}, nil)
		if r.Workload.Status != "signaled" || r.Workload.Signal == nil || *r.Workload.Signal != 9 || r.Workload.ExitCode != nil {
			t.Fatalf("signal %+v", r.Workload)
		}
	})
}
func checkDescendantStopped(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	end := strings.LastIndex(string(stat), ") ")
	if end < 0 || stat[end+2] != 'Z' {
		t.Fatalf("descendant still executing: %s", stat)
	}
}

func TestProcessCanceledPreparationDoesNotLaunch(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	p, _ := execprotocol.NewPayload([]string{binary, fixtureMode, "inspect"}, cwd, 1)
	prepare, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	r := (Executor{HelperPath: binary, Directory: t.TempDir()}).Execute(context.Background(), prepare, p)
	defer r.Remove()
	if r.Workload.Status != "runner_setup_failed" || r.Workload.ExitCode != nil {
		t.Fatalf("canceled preparation %+v", r)
	}
	if _, err := os.Stat(filepath.Join(cwd, "umask-observation")); !os.IsNotExist(err) {
		t.Fatal("canceled setup ran a child")
	}
}
