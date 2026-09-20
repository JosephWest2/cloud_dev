package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/setup"
)

func IsSetupCommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		if !strings.HasPrefix(a, "-") {
			return a == "setup"
		}
		key, _, value := strings.Cut(a, "=")
		if value {
			continue
		}
		switch key {
		case "--config", "--aws-profile", "--region", "--timeout", "--name", "--count", "--group", "--resume", "--retry-missing", "--after", "--ttl", "--cwd", "--exec-timeout", "--delivery-timeout", "--wait-timeout", "--stream", "--stdout-file", "--stderr-file", "--manifest", "--bundle", "--health-timeout":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
		}
	}
	return false
}

type setupRunner func(context.Context, setup.Options, setup.Dependencies) setup.Result

func runSetupCommand(ctx context.Context, args []string, stdout, stderr io.Writer, runner setupRunner) int {
	jsonMode := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" {
			jsonMode = true
		}
	}
	emit := func(r setup.Result) int {
		if jsonMode {
			if err := json.NewEncoder(completeOutput{stdout}).Encode(r); err != nil {
				return 1
			}
		} else {
			if _, err := fmt.Fprintf(completeOutput{stdout}, "%s: %s\n", r.Code, r.Message); err != nil {
				return 1
			}
			if r.SetupID != "" {
				if _, err := fmt.Fprintf(completeOutput{stdout}, "setup=%s phase=%s installation=%s foundation=%s scheduling=%s\n%s\n", r.SetupID, r.Phase, r.Installation, r.Foundation, r.Scheduling, r.Recovery); err != nil {
					return 1
				}
			}
		}
		return r.ExitCode
	}
	fail := func(message string) int {
		return emit(setup.Result{SchemaVersion: 1, Command: "setup", ExitCode: 2, Code: "setup_invalid", Message: message, Completed: []string{}, Checks: []setup.Check{}, Installation: "unverified", Foundation: "unverified", Scheduling: "unverified"})
	}
	options := setup.Options{Version: Version}
	seen := map[string]bool{}
	positionals := []string{}
	help := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			positionals = append(positionals, a)
			continue
		}
		key, val, has := strings.Cut(a, "=")
		if key == "-h" {
			key = "--help"
		}
		if seen[key] {
			return fail("setup options must not be repeated")
		}
		seen[key] = true
		switch key {
		case "--json", "--help":
			if has {
				return fail("setup boolean flags do not accept values")
			}
			if key == "--help" {
				help = true
			}
		case "--config", "--aws-profile", "--region", "--timeout", "--health-timeout", "--manifest", "--bundle", "--resume":
			if !has {
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "--") {
					return fail("setup option requires a value")
				}
				val = args[i]
			}
			if val == "" {
				return fail("setup option values must not be empty")
			}
			switch key {
			case "--config":
				options.ConfigPath = val
			case "--aws-profile":
				options.AWSProfile = val
			case "--region":
				options.Region = val
			case "--manifest":
				options.ManifestPath = val
			case "--bundle":
				options.BundlePath = val
			case "--resume":
				options.Resume = val
			case "--timeout", "--health-timeout":
				d, err := time.ParseDuration(val)
				limit := time.Hour
				if key == "--health-timeout" {
					limit = 30 * time.Minute
				}
				if err != nil || d <= 0 || d > limit {
					return fail("setup timeout must be positive, at most 1h per stage or 30m for health")
				}
				if key == "--timeout" {
					options.StageTimeout = d
				} else {
					options.HealthTimeout = d
				}
			}
		default:
			return fail("unsupported setup option; run devbox setup --help")
		}
	}
	if len(positionals) == 0 || positionals[0] != "setup" {
		return fail("setup command is required")
	}
	if len(positionals) > 1 {
		if len(positionals) != 3 || positionals[1] != "status" {
			return fail("use setup, setup --resume ID, or setup status ID")
		}
		options.Status = positionals[2]
		for key := range seen {
			if key != "--json" && key != "--help" {
				return fail("setup status accepts only --json and --help")
			}
		}
	}
	if options.Resume != "" && (options.ManifestPath != "" || options.ConfigPath != "" || options.AWSProfile != "" || options.Region != "") {
		return fail("resume uses recorded identity and paths; only bundle and timeout options may accompany it")
	}
	if options.ManifestPath != "" && options.BundlePath != "" {
		return fail("existing-manifest connection does not use a provisioning bundle")
	}
	if options.Region != "" && options.Region != "us-east-2" {
		return fail("setup currently supports us-east-2 only")
	}
	if help {
		if jsonMode {
			if err := json.NewEncoder(completeOutput{stdout}).Encode(struct {
				SchemaVersion int    `json:"schema_version"`
				Command       string `json:"command"`
				Help          string `json:"help"`
			}{1, "help", setupHelp}); err != nil {
				return 1
			}
		} else if _, err := fmt.Fprint(completeOutput{stdout}, setupHelp); err != nil {
			return 1
		}
		return 0
	}
	if runner == nil {
		runner = setup.Run
	}
	return emit(runner(ctx, options, setup.Dependencies{Input: os.Stdin, Output: stderr, Terminal: downInputIsTerminal}))
}

const setupHelp = `Usage: devbox setup [options]
       devbox setup --manifest PATH [options]
       devbox setup --resume SETUP_ID [--bundle PATH] [timeout options]
       devbox setup status SETUP_ID [--json]

Guided local configuration and new Ohio foundations. Requires a terminal and
explicit approval of local edits, saved cloud plans and schedule enablement.
No worker is launched. Existing foundation adoption/upgrades remain manual.

  --config PATH          Destination devbox configuration
  --aws-profile NAME     Authenticated source profile (explicitly selected)
  --region REGION        Only us-east-2 is supported
  --manifest PATH        Connect to an existing trusted export; no OpenTofu
  --bundle PATH          Extracted matching release bundle directory
  --resume SETUP_ID      Resume recorded setup; uncertain state is reconciled
  --timeout DURATION     Per-tool stage deadline (default 20m, maximum 1h)
  --health-timeout DURATION  First scheduled-health wait (default 15m, max 30m)
  --json                 One schema-1 result on stdout; prompts on stderr

Status reads recorded local progress only, without AWS or credentials. Journals
and private recovery files live under $XDG_STATE_HOME/devbox/setup (default
~/.local/state/devbox/setup). Setup bundles/tools live under
$XDG_DATA_HOME/devbox (default ~/.local/share/devbox).

Ctrl-C requests graceful tool shutdown. An uncertain apply is never blindly
repeated. Keep the setup ID and workspace; never delete state or force-unlock
to bypass a failed setup. A cleanup-health timeout leaves scheduling enabled
and health pending. Resume observes it. Doctor's full health checks are unchanged.
Exit 0: requested setup complete; 1: failed; 2: invalid/confirmation required;
4: interrupted/timed out. Partial progress and recovery remain in the result.
`
