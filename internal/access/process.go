package access

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

var sessionRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)

const responseEnv = "AWS_SSM_START_SESSION_RESPONSE_DEVBOX"

// Suppress untrusted plugin startup diagnostics before SSH's identification.
// Switch permanently to opaque binary copying; never parse established SSH data.
func copySSH(dst io.Writer, src io.Reader) error { return copySSHReady(dst, src, nil) }
func copySSHReady(dst io.Writer, src io.Reader, ready chan<- struct{}) error {
	b := bufio.NewReaderSize(src, 4096)
	total := 0
	for {
		raw, err := b.ReadSlice('\n')
		line := string(raw)
		total += len(line)
		if total > 16384 {
			return errors.New("plugin startup preamble too large")
		}
		if err != nil {
			return errors.New("plugin ended before SSH identification")
		}
		if strings.HasPrefix(line, "SSH-2.0-") && len(line) <= 255 {
			if _, err = io.WriteString(dst, line); err != nil {
				return err
			}
			if ready != nil {
				close(ready)
			}
			_, err = io.Copy(dst, b)
			return err
		}
	}
}
func childEnv(response []byte) []string {
	env := []string{}
	for _, s := range os.Environ() {
		if !strings.HasPrefix(s, responseEnv+"=") {
			env = append(env, s)
		}
	}
	return append(env, responseEnv+"="+string(response))
}
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *exec.ExitError
	if errors.As(err, &e) && e.ExitCode() >= 0 {
		return e.ExitCode()
	}
	return 255
}
func proxy(ctx, setup context.Context, service *lifecycle.Service, c config.Config, id string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	plugin, err := CheckPlugin(setup)
	if err != nil {
		return 1, err
	}
	fresh, err := service.Resolve(setup, id)
	if err != nil {
		return 1, err
	}
	if fresh[0].State != "running" {
		return 1, fail("instance_not_running", "target stopped before StartSession; inspect the retained instance")
	}
	in := &ssm.StartSessionInput{Target: aws.String(id), DocumentName: aws.String("AWS-StartSSHSession"), Parameters: map[string][]string{"portNumber": {"22"}}}
	out, err := service.SSM.StartSession(setup, in)
	if out != nil && sessionRE.MatchString(aws.ToString(out.SessionId)) {
		sid := aws.ToString(out.SessionId)
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, e := service.SSM.TerminateSession(cleanup, &ssm.TerminateSessionInput{SessionId: aws.String(sid)}); e != nil {
				fmt.Fprintf(stderr, "devbox: SSM session cleanup unverified; run aws ssm terminate-session --session-id %s --region %s with the same profile; instance %s still requires manual teardown\n", sid, c.Region, id)
			}
		}()
	}
	if err != nil {
		return 1, fail("session_unavailable", "SSM could not start the SSH tunnel; check AWS-StartSSHSession/StartSession permissions and SSM connectivity")
	}
	if out == nil || !sessionRE.MatchString(aws.ToString(out.SessionId)) || aws.ToString(out.TokenValue) == "" || aws.ToString(out.StreamUrl) == "" {
		return 1, fail("session_invalid", "SSM returned an incomplete session; retry by instance ID")
	}
	response, _ := json.Marshal(map[string]string{"SessionId": aws.ToString(out.SessionId), "StreamUrl": aws.ToString(out.StreamUrl), "TokenValue": aws.ToString(out.TokenValue)})
	request, _ := json.Marshal(map[string]any{"Target": id, "DocumentName": "AWS-StartSSHSession", "Parameters": map[string][]string{"portNumber": {"22"}}})
	command := exec.CommandContext(ctx, plugin, responseEnv, c.Region, "StartSession", c.AWSProfile, string(request), "https://ssm."+c.Region+".amazonaws.com")
	command.Env = childEnv(response)
	command.Stdin = stdin
	// Kill ordinary plugin credential helpers on exit, including after a timeout.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 2 * time.Second
	pipe, err := command.StdoutPipe()
	if err != nil {
		return 1, fail("plugin_unavailable", "cannot open the SSH tunnel stream")
	}
	if err = command.Start(); err != nil {
		return 1, fail("plugin_unavailable", "cannot start the Session Manager plugin")
	}
	defer syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	copied := make(chan error, 1)
	ready := make(chan struct{})
	go func() { copied <- copySSHReady(stdout, pipe, ready) }()
	// Proxy lifetime is the caller's stdin/SSH lifetime. The outer interactive
	// master bounds authentication; a separate editor enforces ConnectTimeout.
	var copyErr error
	select {
	case copyErr = <-copied:
	case <-ready:
		select {
		case copyErr = <-copied:
		case <-ctx.Done():
			command.Cancel()
			copyErr = <-copied
		}
	case <-setup.Done():
		command.Cancel()
		<-copied
		command.Wait()
		return 4, setup.Err()
	case <-ctx.Done():
		command.Cancel()
		copyErr = <-copied
	}
	if copyErr != nil {
		command.Cancel()
	}
	err = command.Wait()
	if copyErr != nil || err != nil {
		return 255, fail("plugin_connection_failed", "SSM SSH transport failed; verify plugin version, current credentials, signed OpenDataChannel permission and remote sshd; retry by instance ID")
	}
	return 0, nil
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func interactive(ctx, setup context.Context, a Artifacts, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	// Preserve a real terminal descriptor; serialize other writers shared by
	// the master and interactive client (including embedded callers/test buffers).
	if _, ok := stderr.(*os.File); !ok {
		stderr = &lockedWriter{writer: stderr}
	}
	// Use a short private /tmp path to stay within Unix socket path limits even
	// when XDG_STATE_HOME is long. Nothing secret is written to the control file.
	dir, err := os.MkdirTemp("", "devbox-ssh-")
	if err != nil {
		return 1, fail("ssh_local_unavailable", "cannot create private control directory or start OpenSSH")
	}
	defer os.RemoveAll(dir)
	socket := dir + "/control"
	master := exec.CommandContext(ctx, "ssh", "-F", a.ConfigPath, "-M", "-N", "-S", socket, "-o", "ControlPersist=no", a.Alias)
	master.WaitDelay = 7 * time.Second
	master.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	master.Cancel = func() error {
		if master.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-master.Process.Pid, syscall.SIGTERM)
	}
	// The proxy discards provider/plugin stderr and filters startup output.
	// Preserve its sanitized recovery IDs and ordinary OpenSSH diagnostics.
	master.Stderr = stderr
	if err = master.Start(); err != nil {
		fmt.Fprintln(stderr, "devbox: cannot start OpenSSH")
		return 1, fail("ssh_local_unavailable", "cannot create private control directory or start OpenSSH")
	}
	done := make(chan error, 1)
	go func() { done <- master.Wait() }()
	stopped := false
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		exec.CommandContext(closeCtx, "ssh", "-F", a.ConfigPath, "-S", socket, "-O", "exit", a.Alias).Run()
		syscall.Kill(-master.Process.Pid, syscall.SIGTERM)
		if !stopped {
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				master.Process.Kill()
				<-done
			}
		}
		// The proxy supervisor shares the master's group until its own worker
		// has finished bounded SSM cleanup. Reap that group before returning.
		deadline := time.Now().Add(7 * time.Second)
		for syscall.Kill(-master.Process.Pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		syscall.Kill(-master.Process.Pid, syscall.SIGKILL)
	}()
	for {
		if setup.Err() != nil {
			return 4, setup.Err()
		}
		checkCtx, cancel := context.WithTimeout(setup, 500*time.Millisecond)
		e := exec.CommandContext(checkCtx, "ssh", "-F", a.ConfigPath, "-S", socket, "-O", "check", a.Alias).Run()
		cancel()
		if e == nil {
			break
		}
		select {
		case <-setup.Done():
			return 4, setup.Err()
		case <-done:
			stopped = true
			return 255, nil
		case <-time.After(50 * time.Millisecond):
		}
	}
	shell := exec.CommandContext(ctx, "ssh", "-F", a.ConfigPath, "-S", socket, "-o", "ControlMaster=no", "-o", "ProxyCommand=false", "-tt", a.Alias)
	shell.Stdin, shell.Stdout, shell.Stderr = stdin, stdout, stderr
	shell.WaitDelay = 2 * time.Second
	return exitCode(shell.Run()), nil
}
