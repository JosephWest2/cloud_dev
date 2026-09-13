// Package runner executes and publishes one scoped SSM command independently of
// its observing client. Only the workload subprocess runs as the devbox user.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

var Environment = []string{"HOME=/home/devbox", "USER=devbox", "LOGNAME=devbox", "LANG=C.UTF-8", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}

const WorkloadMode = "__devbox_workload"
const searchPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type Captured struct {
	Workload       execprotocol.Workload
	Capture        string
	Stdout, Stderr string
	FinishedAt     time.Time
	Remove         func()
}
type Executor struct {
	HelperPath string
	Directory  string
	Credential *syscall.Credential
	MaxBytes   int64         // Zero selects the production 1 GiB per-stream bound.
	StopGrace  time.Duration // Zero selects the production five-second bound.
}
type launchMessage struct {
	Payload         string `json:"payload"`
	PrepareDeadline int64  `json:"prepare_deadline"`
}

// WorkloadMain is the child entry point after os/exec has changed credentials.
// Private fd3 carries input; fd4 returns only an allowlisted setup byte and is
// closed on exec. Neither descriptor nor any runner environment reaches argv.
func WorkloadMain() int {
	in, status := os.NewFile(3, "payload"), os.NewFile(4, "setup")
	if in == nil || status == nil {
		return 125
	}
	syscall.CloseOnExec(4)
	fail := func(b byte, code int) int { _, _ = status.Write([]byte{b}); _ = status.Close(); return code }
	var m launchMessage
	b, err := io.ReadAll(io.LimitReader(in, execprotocol.MaxEncodedPayload+1025))
	_ = in.Close()
	if err != nil || len(b) > execprotocol.MaxEncodedPayload+1024 || execprotocol.StrictJSON(b, &m) != nil || os.Geteuid() == 0 {
		return fail('S', 125)
	}
	p, err := execprotocol.DecodePayload(m.Payload)
	if err != nil || os.Chdir(p.Cwd) != nil {
		return fail('S', 125)
	}
	syscall.Umask(022)
	executable := p.Argv[0]
	if !strings.Contains(executable, "/") {
		found := ""
		denied := false
		for _, dir := range strings.Split(searchPath, ":") {
			candidate := filepath.Join(dir, executable)
			st, e := os.Stat(candidate)
			if e != nil {
				if !os.IsNotExist(e) {
					denied = true
				}
				continue
			}
			if st.IsDir() || syscall.Access(candidate, 1) != nil {
				denied = true
				continue
			}
			found = candidate
			break
		}
		if found == "" {
			if denied {
				return fail('X', 126)
			}
			return fail('N', 127)
		}
		executable = found
	}
	if m.PrepareDeadline <= 0 || time.Now().UnixNano() >= m.PrepareDeadline {
		return fail('S', 125)
	}
	if err := syscall.Exec(executable, p.Argv, Environment); err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return fail('N', 127)
		}
		return fail('X', 126)
	}
	return 125
}

type captureResult struct{ err error }

func capture(pipe *os.File, file *os.File, limit int64, failed chan<- struct{}, done chan<- captureResult) {
	var stored int64
	var first error
	mark := func(err error) {
		if first == nil {
			first = err
			select {
			case failed <- struct{}{}:
			default:
			}
		}
	}
	b := make([]byte, 64*1024)
	for {
		n, err := pipe.Read(b)
		if n > 0 {
			keep := int64(n)
			overflow := keep > limit-stored
			if overflow {
				keep = limit - stored
			}
			if keep > 0 && first == nil {
				written, e := file.Write(b[:keep])
				stored += int64(written)
				if e != nil || int64(written) != keep {
					mark(errors.New("capture write failed"))
				}
			}
			if overflow {
				mark(errors.New("capture limit exceeded"))
			}
		}
		if err != nil {
			if err != io.EOF {
				mark(err)
			}
			break
		}
	}
	if err := file.Sync(); err != nil {
		mark(err)
	}
	_ = pipe.Close()
	done <- captureResult{first}
}

func (e Executor) Execute(ctx, prepare context.Context, p execprotocol.Payload) Captured {
	r := Captured{Workload: execprotocol.Workload{Status: "runner_setup_failed"}, Capture: "complete", Remove: func() {}}
	dir, err := os.MkdirTemp(e.Directory, "command-")
	if err != nil {
		r.Capture = "incomplete"
		r.FinishedAt = time.Now()
		return r
	}
	r.Remove = func() { _ = os.RemoveAll(dir) }
	r.Stdout, r.Stderr = filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	files := []*os.File{}
	reads := []*os.File{}
	writes := []*os.File{}
	defer func() {
		for _, f := range append(append(files, reads...), writes...) {
			_ = f.Close()
		}
	}()
	setupFail := func() Captured { r.FinishedAt = time.Now(); return r }
	for _, name := range []string{r.Stdout, r.Stderr} {
		f, e := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			r.Capture = "incomplete"
			return setupFail()
		}
		files = append(files, f)
		read, write, e := os.Pipe()
		if e != nil {
			r.Capture = "incomplete"
			return setupFail()
		}
		reads = append(reads, read)
		writes = append(writes, write)
	}
	if prepare.Err() != nil || ctx.Err() != nil {
		return setupFail()
	}
	payload, err := execprotocol.EncodePayload(p)
	if err != nil {
		return setupFail()
	}
	deadline, ok := prepare.Deadline()
	if !ok {
		return setupFail()
	}
	msg, _ := json.Marshal(launchMessage{payload, deadline.UnixNano()})
	input, err := os.CreateTemp(dir, "payload-")
	if err != nil {
		return setupFail()
	}
	defer input.Close()
	if _, err = input.Write(msg); err != nil {
		return setupFail()
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return setupFail()
	}
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		return setupFail()
	}
	defer statusRead.Close()
	defer statusWrite.Close()
	cmd := exec.Command(e.HelperPath, WorkloadMode)
	cmd.Env = append([]string(nil), Environment...)
	cmd.ExtraFiles = []*os.File{input, statusWrite}
	cmd.Stdout, cmd.Stderr = writes[0], writes[1]
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: e.Credential}
	if err = cmd.Start(); err != nil {
		return setupFail()
	}
	_ = statusWrite.Close()
	_ = writes[0].Close()
	_ = writes[1].Close()
	limit := e.MaxBytes
	if limit <= 0 || limit > execprotocol.MaxStreamBytes {
		limit = execprotocol.MaxStreamBytes
	}
	grace := e.StopGrace
	if grace <= 0 || grace > 5*time.Second {
		grace = 5 * time.Second
	}
	failed := make(chan struct{}, 1)
	captured := make(chan captureResult, 2)
	for n := range reads {
		go capture(reads[n], files[n], limit, failed, captured)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	ready := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(io.LimitReader(statusRead, 2)); ready <- b }()
	var status []byte
	stopped := false
	timedOut := false
	captureFailed := false
	interrupted := false
	select {
	case status = <-ready:
	case <-prepare.Done():
		stopped = true
	case <-ctx.Done():
		stopped = true
		interrupted = true
	}
	var waitErr error
	childDone := false
	if !stopped {
		work, stop := context.WithTimeout(ctx, time.Duration(p.ExecTimeoutSeconds)*time.Second)
		select {
		case waitErr = <-waited:
			childDone = true
		case <-failed:
			captureFailed = true
		case <-work.Done():
			timedOut = ctx.Err() == nil
			interrupted = ctx.Err() != nil
		}
		stop()
	}
	// Stop ordinary descendants even after the direct child has exited. Detached
	// descendants may retain pipes; the bounded drain below cannot certify them.
	if syscall.Kill(-cmd.Process.Pid, 0) == nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(grace)
		tick := time.NewTicker(20 * time.Millisecond)
	stopGroup:
		for {
			select {
			case <-tick.C:
				if syscall.Kill(-cmd.Process.Pid, 0) != nil {
					break stopGroup
				}
			case <-timer.C:
				break stopGroup
			}
		}
		_ = timer.Stop()
		tick.Stop()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if !childDone {
		select {
		case waitErr = <-waited:
			childDone = true
		case <-time.After(grace):
			r.Capture = "incomplete"
		}
	}
	drain := time.NewTimer(grace)
	defer drain.Stop()
	for remaining := 2; remaining > 0; {
		select {
		case c := <-captured:
			remaining--
			if c.err != nil {
				r.Capture = "incomplete"
			}
		case <-drain.C:
			r.Capture = "incomplete"
			for _, pipe := range reads {
				_ = pipe.Close()
			}
			// Closing a pipe releases its read goroutine; only bounded local file
			// writes remain. Freeze all writers before hashing/uploading the files.
			for ; remaining > 0; remaining-- {
				<-captured
			}
		}
	}
	r.Workload = execprotocol.Workload{Status: "exited", ExitCode: intValue(0)}
	if waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			state, ok := exit.Sys().(syscall.WaitStatus)
			if ok && state.Signaled() {
				r.Workload = execprotocol.Workload{Status: "signaled", Signal: intValue(int(state.Signal()))}
			} else if exit.ExitCode() >= 0 {
				r.Workload.ExitCode = intValue(exit.ExitCode())
			} else {
				r.Workload = execprotocol.Workload{Status: "unknown"}
			}
		} else {
			r.Workload = execprotocol.Workload{Status: "unknown"}
		}
	}
	switch {
	case timedOut:
		r.Workload.Status = "execution_timeout"
		r.Workload.ExitCode = nil
	case captureFailed:
		r.Workload.Status = "capture_failed"
		r.Workload.ExitCode = nil
		r.Capture = "incomplete"
	case interrupted || !childDone:
		r.Workload = execprotocol.Workload{Status: "unknown"}
	case stopped || len(status) > 0 && status[0] == 'S':
		r.Workload = execprotocol.Workload{Status: "runner_setup_failed"}
	case len(status) > 0 && status[0] == 'N':
		r.Workload = execprotocol.Workload{Status: "exited", ExitCode: intValue(127)}
	case len(status) > 0 && status[0] == 'X':
		r.Workload = execprotocol.Workload{Status: "exited", ExitCode: intValue(126)}
	}
	r.FinishedAt = time.Now()
	return r
}
func intValue(n int) *int { return &n }
