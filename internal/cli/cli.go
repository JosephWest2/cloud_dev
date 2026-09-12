package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/access"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"os"
)

const help = `Usage: devbox [options] doctor
       devbox [options] up agent --on-demand --name NAME
       devbox [options] up --resume REQUEST_ID
       devbox [options] ls
       devbox [options] down NAME_OR_INSTANCE_ID
       devbox [options] ssh NAME_OR_INSTANCE_ID
       devbox [options] ssh-config NAME_OR_INSTANCE_ID
       devbox [options] proxy INSTANCE_ID
       devbox [--json] version

Options may appear before or after the command:
  --config PATH         User TOML (default: $XDG_CONFIG_HOME/devbox/config.toml
                        or ~/.config/devbox/config.toml)
  --aws-profile NAME    AWS profile (overrides AWS_PROFILE and user TOML)
  --region REGION       Explicit region (overrides user TOML)
  --timeout DURATION    Setup deadline (up/access: 5m; others: 20s; max: 5m)
  --name NAME           Friendly name for a new launch
  --on-demand           Explicitly select supported On-Demand purchasing
  --resume REQUEST_ID   Reconcile or resume a durable launch request
  --json                One versioned JSON result on stdout
  --help, -h            Show this help

doctor checks local setup and AWS identity without provisioning resources.
up launches one instance; Spot is unsupported. ls/down use AWS inventory.
up waits for EC2 + SSM + bootstrap readiness. ssh uses real SSH over SSM.
ssh-config supports editors/scp/sftp; proxy is its transport helper.
ssh/proxy reject --json. Failed/finished sessions retain workers: run down.
`

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, deps doctor.Dependencies) int {
	return RunWithLifecycle(ctx, args, stdout, stderr, deps, lifecycle.Dependencies{})
}

func RunWithLifecycle(ctx context.Context, args []string, stdout, stderr io.Writer, deps doctor.Dependencies, life lifecycle.Dependencies) int {
	jsonMode := false
	// Recognize JSON even on invalid invocations; never echo untrusted arguments.
	for _, a := range args {
		if a == "--json" {
			jsonMode = true
		}
	}
	fail := func(message string) int {
		r := doctor.Result{SchemaVersion: 1, Command: "", OK: false, ExitCode: doctor.ExitConfig, Checks: []doctor.Check{{Name: "usage", Status: "fail", Code: "usage_invalid", Message: message}}}
		return emit(r, jsonMode, stdout, stderr)
	}
	var command, path string
	var positional []string
	var launch lifecycle.UpOptions
	var overrides config.Overrides
	timeout := 20 * time.Second
	helpMode := false
	timeoutSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--on-demand" {
			launch.OnDemand = true
			continue
		}
		if a == "--spot" {
			return fail("Spot is unsupported; use up agent --on-demand --name NAME")
		}
		if a == "--json" {
			continue
		}
		if a == "--help" || a == "-h" {
			helpMode = true
			continue
		}
		key, val, hasVal := strings.Cut(a, "=")
		switch key {
		case "--config", "--aws-profile", "--region", "--timeout", "--name", "--resume":
			if !hasVal {
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "--") {
					return fail("an option requires a value; run devbox --help")
				}
				val = args[i]
			}
			if val == "" {
				return fail("option values must not be empty; run devbox --help")
			}
			switch key {
			case "--name":
				launch.Name = val
			case "--resume":
				launch.Resume = val
			case "--config":
				path = val
			case "--aws-profile":
				overrides.AWSProfile = val
			case "--region":
				overrides.Region = val
			case "--timeout":
				timeoutSet = true
				var err error
				timeout, err = time.ParseDuration(val)
				if err != nil || timeout <= 0 || timeout > 5*time.Minute {
					return fail("--timeout must be a positive duration no greater than 5m, such as 20s")
				}
			}
		default:
			if strings.HasPrefix(a, "-") {
				return fail("unknown option or extra argument; run devbox --help")
			}
			if command == "" {
				command = a
			} else {
				positional = append(positional, a)
			}
		}
	}
	if helpMode || len(args) == 0 {
		if jsonMode {
			if err := json.NewEncoder(stdout).Encode(struct {
				SchemaVersion int    `json:"schema_version"`
				Command       string `json:"command"`
				OK            bool   `json:"ok"`
				ExitCode      int    `json:"exit_code"`
				Help          string `json:"help"`
			}{1, "help", true, 0, help}); err != nil {
				return doctor.ExitPrerequisite
			}
		} else {
			if _, err := io.WriteString(stdout, help); err != nil {
				return doctor.ExitPrerequisite
			}
		}
		return 0
	}
	if command != "up" && (launch.OnDemand || launch.Name != "" || launch.Resume != "") {
		return fail("launch options require up; run devbox --help")
	}
	isAccess := command == "ssh" || command == "ssh-config" || command == "proxy"
	if command != "up" && command != "down" && !isAccess && len(positional) > 0 {
		return fail("extra argument; run devbox --help")
	}
	if command == "up" {
		if launch.Resume != "" {
			if !lifecycle.ValidRequest(launch.Resume) || len(positional) > 0 || launch.OnDemand || launch.Name != "" {
				return fail("use up --resume REQUEST_ID without launch parameters")
			}
		} else if len(positional) != 1 || positional[0] != "agent" || !lifecycle.ValidName(launch.Name) {
			return fail("use up agent --on-demand --name NAME")
		}
	}
	if isAccess && (len(positional) != 1 || !lifecycle.ValidTarget(positional[0])) {
		return fail("use ssh/ssh-config/proxy with one friendly name or instance ID")
	}
	if (command == "ssh" || command == "proxy") && jsonMode {
		return fail("interactive ssh and proxy reject --json; use ssh-config --json for separate configuration metadata")
	}
	if command == "down" && (len(positional) != 1 || !lifecycle.ValidTarget(positional[0])) {
		return fail("use down with one friendly name or instance ID")
	}
	if command == "version" {
		if jsonMode {
			if err := json.NewEncoder(stdout).Encode(struct {
				SchemaVersion int    `json:"schema_version"`
				Command       string `json:"command"`
				OK            bool   `json:"ok"`
				ExitCode      int    `json:"exit_code"`
				Version       string `json:"version"`
			}{1, "version", true, 0, "0.1.0-dev"}); err != nil {
				return doctor.ExitPrerequisite
			}
		} else {
			if _, err := fmt.Fprintln(stdout, "devbox 0.1.0-dev"); err != nil {
				return doctor.ExitPrerequisite
			}
		}
		return 0
	}
	if command != "doctor" && command != "up" && command != "ls" && command != "down" && !isAccess {
		return fail("unknown or missing command; available commands: doctor, up, ls, down, ssh, ssh-config, proxy, version; run devbox --help")
	}
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return fail(err.Error())
		}
	}

	if !timeoutSet && (command == "up" || isAccess) {
		timeout = 5 * time.Minute
	}
	if isAccess {
		r := access.Run(ctx, access.Options{Command: command, Target: positional[0], ConfigPath: path, Overrides: overrides, Timeout: timeout}, os.Stdin, stdout, stderr, access.Dependencies{New: life.New})
		if command == "ssh-config" {
			if jsonMode {
				if json.NewEncoder(stdout).Encode(r) != nil {
					return 1
				}
			} else if r.OK {
				if _, err := io.WriteString(stdout, r.SSHConfig); err != nil {
					return 1
				}
			} else {
				emitLifecycle(r.Result, false, stderr, stderr)
			}
		} else if !r.OK {
			emitLifecycle(r.Result, false, stderr, stderr)
		}
		return r.ExitCode
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if command != "doctor" {
		opts := lifecycle.Options{Command: command, UpOptions: launch}
		if command == "down" {
			opts.Target = positional[0]
		}
		return emitLifecycle(lifecycle.Run(ctx, path, overrides, opts, life, stderr), jsonMode, stdout, stderr)
	}
	return emit(doctor.Run(ctx, path, overrides, deps), jsonMode, stdout, stderr)
}

func emit(r doctor.Result, jsonMode bool, stdout, stderr io.Writer) int {
	if jsonMode {
		if err := json.NewEncoder(stdout).Encode(r); err != nil {
			fmt.Fprintln(stderr, "cannot write command result")
			return doctor.ExitPrerequisite
		}
	} else {
		for _, c := range r.Checks {
			if _, err := fmt.Fprintf(stdout, "%s %s: %s\n", c.Status, c.Name, c.Message); err != nil {
				return doctor.ExitPrerequisite
			}
		}
	}
	if !r.OK {
		fmt.Fprintln(stderr, "devbox: checks failed; follow the actions in the command result")
	}
	return r.ExitCode
}

func emitLifecycle(r lifecycle.Result, jsonMode bool, stdout, stderr io.Writer) int {
	if jsonMode {
		if err := json.NewEncoder(stdout).Encode(r); err != nil {
			fmt.Fprintln(stderr, "cannot write command result")
			return doctor.ExitPrerequisite
		}
	} else {
		if _, err := fmt.Fprintf(stdout, "%s: %s\n", r.Code, r.Message); err != nil {
			return doctor.ExitPrerequisite
		}
		if r.RequestID != "" {
			if _, err := fmt.Fprintf(stdout, "request=%s receipt=%q\n", r.RequestID, r.ReceiptPath); err != nil {
				return doctor.ExitPrerequisite
			}
		}
		for _, i := range r.Instances {
			if _, err := fmt.Fprintf(stdout, "%s name=%q request=%q image=%s type=%s market=%s template=%s/%s ec2=%s ssm=%s bootstrap=%s readiness=%s root_deletion=%s\n", i.ID, i.Name, i.RequestID, i.Image, i.Type, i.Market, i.TemplateID, i.TemplateVersion, i.State, i.SSM, i.Bootstrap, i.Readiness, i.RootDeletion); err != nil {
				return doctor.ExitPrerequisite
			}
			if i.ProbeCommandID != "" || i.ObservationCode != "" {
				fmt.Fprintf(stdout, "  probe_command=%s observation=%s\n", i.ProbeCommandID, i.ObservationCode)
			}
			for _, v := range i.Volumes {
				if _, err := fmt.Fprintf(stdout, "  volume=%s device=%q root=%t delete_on_termination=%t deletion=%s\n", v.ID, v.Device, v.Root, v.DeleteOnTermination, v.Deletion); err != nil {
					return doctor.ExitPrerequisite
				}
			}
		}
	}
	if !r.OK {
		prefix := r.RecoveryPrefix
		if prefix == "" {
			prefix = "devbox"
		}
		fmt.Fprintf(stderr, "devbox: %s: %s\n", r.Code, r.Message)
		for _, i := range r.Instances {
			fmt.Fprintf(stderr, "devbox: retained instance %s; inspect: %s ls --json; cleanup: %s down %s --timeout 5m (manual cleanup until TTL ships)\n", i.ID, prefix, prefix, i.ID)
		}
		if r.RequestID != "" {
			if lifecycle.ValidRequest(r.RequestID) {
				fmt.Fprintf(stderr, "devbox: request %s; recover with %s up --resume %s\n", r.RequestID, prefix, r.RequestID)
			}
		}
	}
	return r.ExitCode
}
