package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
)

const help = `Usage: devbox [options] doctor
       devbox [--json] version

Options may appear before or after the command:
  --config PATH         User TOML (default: $XDG_CONFIG_HOME/devbox/config.toml
                        or ~/.config/devbox/config.toml)
  --aws-profile NAME    AWS profile (overrides AWS_PROFILE and user TOML)
  --region REGION       Explicit region (overrides user TOML)
  --timeout DURATION    Check deadline (default: 20s; maximum: 5m)
  --json                One versioned JSON result on stdout
  --help, -h            Show this help

doctor checks local setup and AWS identity without provisioning resources.
Launch, inventory, shell and teardown commands arrive in later MVP issues.
`

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, deps doctor.Dependencies) int {
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
	var overrides config.Overrides
	timeout := 20 * time.Second
	helpMode := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" {
			continue
		}
		if a == "--help" || a == "-h" {
			helpMode = true
			continue
		}
		key, val, hasVal := strings.Cut(a, "=")
		switch key {
		case "--config", "--aws-profile", "--region", "--timeout":
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
			case "--config":
				path = val
			case "--aws-profile":
				overrides.AWSProfile = val
			case "--region":
				overrides.Region = val
			case "--timeout":
				var err error
				timeout, err = time.ParseDuration(val)
				if err != nil || timeout <= 0 || timeout > 5*time.Minute {
					return fail("--timeout must be a positive duration no greater than 5m, such as 20s")
				}
			}
		default:
			if strings.HasPrefix(a, "-") || command != "" {
				return fail("unknown option or extra argument; run devbox --help")
			}
			command = a
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
	if command != "doctor" {
		return fail("unknown or missing command; available commands: doctor, version; run devbox --help")
	}
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return fail(err.Error())
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
