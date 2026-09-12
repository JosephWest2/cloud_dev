package access

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
)

type Options struct {
	Command, Target, ConfigPath string
	Overrides                   config.Overrides
	Timeout                     time.Duration
}
type Dependencies struct {
	New           func(context.Context, config.Config) (*lifecycle.Service, error)
	Prerequisites func(context.Context, config.Config, config.Manifest) error
	Shell         func(context.Context, context.Context, Artifacts, io.Reader, io.Writer, io.Writer) int
}
type Result struct {
	lifecycle.Result
	SSHConfig     string `json:"ssh_config,omitempty"`
	SSHConfigPath string `json:"ssh_config_path,omitempty"`
	SSHHost       string `json:"ssh_host,omitempty"`
}

func Run(ctx context.Context, o Options, stdin io.Reader, stdout, stderr io.Writer, deps Dependencies) Result {
	r := Result{Result: lifecycle.Result{SchemaVersion: 1, Command: o.Command, Outcome: lifecycle.Outcome{Instances: []lifecycle.Instance{}}}}
	setup, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	failure := func(err error) Result {
		r.OK = false
		r.ExitCode = 1
		r.Code = "connection_unavailable"
		r.Message = "connection setup failed; check credentials, prerequisites and permissions"
		var f *lifecycle.Failure
		var id *identity.Failure
		if errors.As(err, &f) {
			r.Code, r.Message = f.Code, f.Message
		}
		if errors.As(err, &id) {
			r.Code, r.Message = id.Code, id.Message
		}
		if setup.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			r.ExitCode = 4
			r.Code = "connection_timeout"
			r.Message = "connection setup timed out or was canceled; the instance is retained; inspect/retry by ID or tear it down"
		}
		return r
	}
	c, err := config.Load(o.ConfigPath, o.Overrides)
	if err != nil {
		r = failure(fail("config_invalid", err.Error()))
		r.ExitCode = 2
		return r
	}
	m, err := config.LoadManifest(c.Manifest, c, config.Profile{Image: "agent"})
	if err != nil {
		return failure(fail("manifest_invalid", err.Error()))
	}
	pre := deps.Prerequisites
	if pre == nil {
		pre = prerequisites
	}
	if err = pre(setup, c, m); err != nil {
		return failure(err)
	}
	factory := deps.New
	if factory == nil {
		factory = lifecycle.New
	}
	service, err := factory(setup, c)
	if err != nil {
		return failure(err)
	}
	r.Instances, err = service.Resolve(setup, o.Target)
	if err != nil {
		return failure(err)
	}
	i := &r.Instances[0]
	r.RequestID = i.RequestID
	fmt.Fprintf(stderr, "devbox: selected instance %s; inspect: devbox ls; retry: devbox ssh %s; teardown: devbox down %s --timeout 5m (use the same config/profile/region; manual cleanup is required)\n", i.ID, i.ID, i.ID)
	if err = service.WaitReady(setup, m, i, stderr); err != nil {
		return failure(err)
	}
	executable, err := os.Executable()
	if err != nil {
		return failure(fail("executable_unavailable", "cannot locate devbox executable"))
	}
	a, err := artifacts(setup, c, *i, o.ConfigPath, executable)
	if err != nil {
		return failure(err)
	}
	switch o.Command {
	case "ssh-config":
		r.SSHConfig, r.SSHConfigPath, r.SSHHost = a.Config, a.ConfigPath, a.Alias
	case "proxy":
		code, e := proxy(ctx, setup, service, c, i.ID, stdin, stdout, stderr)
		if e != nil {
			return failure(e)
		}
		r.ExitCode = code
	case "ssh":
		shell := deps.Shell
		if shell == nil {
			shell = interactive
		}
		r.ExitCode = shell(ctx, setup, a, stdin, stdout, stderr)
		if r.ExitCode == 4 {
			return failure(context.DeadlineExceeded)
		}
	default:
		return failure(fail("usage_invalid", "unknown access command"))
	}
	r.OK = r.ExitCode == 0
	r.Status = "session_closed"
	r.Code = r.Status
	r.Message = "SSH session closed; the instance remains allocated and requires manual cleanup"
	if o.Command == "ssh-config" {
		r.Status = "ssh_configured"
		r.Code = r.Status
		r.Message = "use the generated file with ssh/scp/sftp -F or your remote editor's SSH config setting"
	}
	if !r.OK {
		r.Code = "ssh_failed"
		r.Message = "OpenSSH failed or the remote shell returned a nonzero exit status; for authentication failure load the matching key with ssh-add; inspect/retry or tear down the retained instance"
	}
	return r
}
