// Package doctor performs independent, read-only setup checks.
package doctor

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/foundation"
	"github.com/JosephWest2/cloud_dev/internal/identity"
)

const (
	ExitOK           = 0
	ExitPrerequisite = 1
	ExitConfig       = 2
	ExitRemote       = 3 // Reserved for a completed remote command that failed.
	ExitTimeout      = 4
	ExitPartial      = 5 // Reserved for partial batch completion.
)

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Result struct {
	SchemaVersion int     `json:"schema_version"`
	Command       string  `json:"command"`
	OK            bool    `json:"ok"`
	ExitCode      int     `json:"exit_code"`
	Checks        []Check `json:"checks"`
}

type Dependencies struct {
	Identity   func(context.Context, config.Config) error
	Foundation func(context.Context, config.Config, config.Manifest, config.Profile) []foundation.Check
	Plugin     func(context.Context) error
	SSH        func(context.Context) error
	GOOS       string
}

func DefaultDependencies() Dependencies {
	return Dependencies{Identity: identity.Check, Foundation: foundation.CheckDeployment, Plugin: CheckPlugin, SSH: CheckSSH, GOOS: runtime.GOOS}
}

func CheckSSH(ctx context.Context) error {
	path, err := exec.LookPath("ssh")
	if err != nil {
		return errors.New("install the OpenSSH client (on Arch: sudo pacman -S openssh) and put ssh on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = exec.CommandContext(ctx, path, "-V").Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("OpenSSH client could not run; verify ssh -V and repair the installation")
	}
	return nil
}

func CheckPlugin(ctx context.Context) error {
	path, err := exec.LookPath("session-manager-plugin")
	if err != nil {
		return errors.New("install the AWS Session Manager plugin and put session-manager-plugin on PATH; see README prerequisites")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Discard subprocess streams, which are outside our diagnostic contract.
	if err = exec.CommandContext(ctx, path, "--version").Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Session Manager plugin could not run; verify session-manager-plugin --version and reinstall if needed")
	}
	return nil
}

func Run(ctx context.Context, path string, overrides config.Overrides, deps Dependencies) Result {
	r := Result{SchemaVersion: 1, Command: "doctor", OK: true, Checks: []Check{}}
	add := func(name, status, code, message string, exit int) {
		r.Checks = append(r.Checks, Check{name, status, code, message})
		if exit != 0 {
			r.OK = false
			if r.ExitCode == 0 || exit == ExitConfig || (exit == ExitTimeout && r.ExitCode != ExitConfig) {
				r.ExitCode = exit
			}
		}
	}
	probe := func(name, code, failure, success string, check func(context.Context) error) {
		if ctx.Err() != nil {
			add(name, "skip", "check_canceled", "check not run because the deadline elapsed or the command was canceled; retry with --timeout 60s", ExitTimeout)
			return
		}
		err := check(ctx)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			add(name, "fail", "probe_timeout", "local executable probe timed out or was canceled; retry and verify its version command completes", ExitTimeout)
		} else if err != nil {
			add(name, "fail", code+"_unavailable", failure, ExitPrerequisite)
		} else {
			add(name, "pass", code+"_available", success, 0)
		}
	}
	c, configErr := config.Load(path, overrides)
	if configErr != nil {
		add("configuration", "fail", "config_invalid", configErr.Error(), ExitConfig)
		add("profile", "skip", "config_required", "fix configuration to validate the agent profile", 0)
		add("manifest", "skip", "config_required", "fix configuration, then export the deployment manifest from the foundation (issue #7)", 0)
		add("aws_identity", "skip", "config_required", "fix configuration before AWS identity verification", 0)
	} else {
		add("configuration", "pass", "config_valid", "version 1 configuration and explicit account/region/deployment/owner scope are valid", 0)
		p, err := config.LoadProfile(c.ProfileFile)
		if err != nil {
			add("profile", "fail", "profile_invalid", err.Error(), ExitConfig)
			add("manifest", "skip", "profile_required", "fix the profile before validating its deployment image", 0)
			add("aws_identity", "skip", "profile_required", "fix the profile before AWS identity verification", 0)
		} else {
			add("profile", "pass", "profile_valid", "agent profile is valid; Spot defaults are preserved; initial launches will require explicit --on-demand until Spot support ships", 0)
			m, manifestErr := config.LoadManifest(c.Manifest, c, p)
			if err := manifestErr; err != nil {
				add("manifest", "fail", "manifest_unavailable", err.Error(), ExitPrerequisite)
			} else {
				add("manifest", "pass", "manifest_valid", "version 4 deployment manifest schema, scope and exact resource pins are valid", 0)
			}
			if err := deps.Identity(ctx, c); err != nil {
				code, message, exit := "identity_unavailable", "cannot verify AWS identity; check the selected profile, credentials and connectivity", ExitPrerequisite
				var f *identity.Failure
				if errors.As(err, &f) {
					code, message = f.Code, f.Message
					if code == "timeout" {
						exit = ExitTimeout
					}
				}
				add("aws_identity", "fail", code, message, exit)
				add("foundation", "skip", "identity_required", "verify the expected AWS account before deployed-resource checks", 0)
			} else {
				add("aws_identity", "pass", "account_verified", "AWS credentials are valid and match expected_account", 0)
				if manifestErr != nil {
					add("foundation", "skip", "manifest_required", "export a valid matching manifest before deployed-resource checks", 0)
				} else if ctx.Err() != nil {
					add("foundation", "skip", "check_canceled", "resource checks canceled; retry with --timeout 60s", ExitTimeout)
				} else if deps.Foundation == nil {
					add("foundation", "fail", "foundation_unavailable", "deployed-resource checker unavailable; reinstall devbox", ExitPrerequisite)
				} else {
					checks := deps.Foundation(ctx, c, m, p)
					if len(checks) == 0 {
						add("foundation", "fail", "foundation_unavailable", "deployed-resource checks returned no results; reinstall devbox", ExitPrerequisite)
					}
					for _, check := range checks {
						if errors.Is(check.Err, context.Canceled) || errors.Is(check.Err, context.DeadlineExceeded) {
							add(check.Name, "fail", "foundation_timeout", "deployed-resource check timed out or was canceled; retry with --timeout 60s", ExitTimeout)
						} else if check.Err != nil {
							// Dependency output is untrusted; only package-generated allowlisted messages escape.
							add(check.Name, "fail", "foundation_drift", foundation.Message(check.Name), ExitPrerequisite)
						} else {
							add(check.Name, "pass", "foundation_verified", "deployed resource settings match the trusted foundation export", 0)
						}
					}
				}
			}
		}
	}
	if deps.GOOS != "linux" {
		add("platform", "fail", "platform_unsupported", "the initial local platform is Linux (Arch); other platforms are not yet supported", ExitPrerequisite)
	} else {
		add("platform", "pass", "platform_supported", "local platform is Linux; initial development target is Arch", 0)
	}
	probe("session_manager_plugin", "plugin", "install or repair the AWS Session Manager plugin on PATH and verify session-manager-plugin --version; see README prerequisites", "Session Manager plugin is available and executable", deps.Plugin)
	probe("openssh_client", "ssh", "install or repair the OpenSSH client on PATH (on Arch: sudo pacman -S openssh) and verify ssh -V", "SSH client is executable; run ssh-config for scoped editor and file-transfer access", deps.SSH)
	return r
}
