package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
)

// IsCleanupCommand recognizes the command while skipping option values. Process
// entry uses the same classification to handle cleanup output errors without
// changing other commands' signal behavior. This also selects the
// cleanup error envelope when an earlier global option has an invalid value.
// The first positional command and exec's separator stop the scan.
func IsCleanupCommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		if !strings.HasPrefix(a, "-") {
			return a == "cleanup"
		}
		key, _, hasValue := strings.Cut(a, "=")
		if hasValue {
			continue
		}
		switch key {
		case "--config", "--aws-profile", "--region", "--timeout", "--name", "--count", "--group", "--resume", "--retry-missing", "--after", "--ttl", "--cwd", "--exec-timeout", "--delivery-timeout", "--wait-timeout", "--stream", "--stdout-file", "--stderr-file":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
		}
	}
	return false
}

func runCleanupCommand(ctx context.Context, args []string, stdout, stderr io.Writer, runner cleanupRunner) int {
	jsonMode := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" {
			jsonMode = true
		}
	}
	output := newCleanupOutput(stderr)
	dry := false
	fail := func(message string) int {
		return emitCleanup(cleanupFailure(expiry.Scope{}, dry, "cleanup_invalid", message, 2), jsonMode, stdout, output)
	}
	var path string
	var overrides config.Overrides
	timeout := cleanupTimeout
	seen := map[string]bool{}
	commandSeen, helpMode := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "cleanup" && !commandSeen {
			commandSeen = true
			continue
		}
		key, value, hasValue := strings.Cut(a, "=")
		if key == "-h" {
			key = "--help"
		}
		if seen[key] {
			return fail("cleanup options must not be repeated; run devbox cleanup --help")
		}
		seen[key] = true
		switch key {
		case "--json", "--dry-run", "--help":
			if hasValue {
				return fail("cleanup boolean flags do not accept values")
			}
			switch key {
			case "--dry-run":
				dry = true
			case "--help":
				helpMode = true
			}
		case "--config", "--aws-profile", "--region", "--timeout":
			if !hasValue {
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "--") {
					return fail("cleanup option requires a value")
				}
				value = args[i]
			}
			if value == "" {
				return fail("cleanup option values must not be empty")
			}
			switch key {
			case "--config":
				path = value
			case "--aws-profile":
				overrides.AWSProfile = value
			case "--region":
				overrides.Region = value
			case "--timeout":
				duration, err := time.ParseDuration(value)
				if err != nil || duration <= 0 || duration > 5*time.Minute {
					return fail("--timeout must be positive and no greater than 5m; cleanup defaults to 165s")
				}
				timeout = duration
			}
		default:
			return fail("cleanup accepts only --dry-run, --json, --timeout and global config/profile/region options; no targets, selectors, --yes or launch flags")
		}
	}
	if !commandSeen {
		return fail("cleanup command is required")
	}
	if helpMode {
		if jsonMode {
			if err := json.NewEncoder(completeOutput{stdout}).Encode(struct {
				SchemaVersion int    `json:"schema_version"`
				Command       string `json:"command"`
				OK            bool   `json:"ok"`
				ExitCode      int    `json:"exit_code"`
				Help          string `json:"help"`
			}{1, "help", true, 0, cleanupHelp}); err != nil {
				return 1
			}
		} else if _, err := fmt.Fprint(completeOutput{stdout}, cleanupHelp); err != nil {
			return 1
		}
		return 0
	}
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return fail(err.Error())
		}
	}
	ctx = prepareCommandLogin(ctx, path, overrides, "cleanup", jsonMode, stderr)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if runner == nil {
		runner = func(ctx context.Context, path string, overrides config.Overrides, dry bool, diagnostics io.Writer) expiry.Result {
			return runCleanup(ctx, path, overrides, dry, diagnostics, cleanupDependencies{})
		}
	}
	return emitCleanup(runner(ctx, path, overrides, dry, output), jsonMode, stdout, output)
}

const cleanupHelp = `Usage: devbox [options] cleanup [--dry-run] [--json] [--timeout DURATION]

Evaluate expiry in the configured expected account, explicit region, deployment
and exact owner. Invoking cleanup authorizes the expired-worker policy without
another prompt. No targets, group/all selectors, --yes or launch flags are accepted.

  --dry-run             Advisory discovery and reasons; no writes or termination
  --json                One schema-1 result on stdout; evidence goes to stderr
  --timeout DURATION    Positive, at most 5m; default and service ceiling 165s
  --config PATH         User TOML with trusted AWS scope
  --aws-profile NAME    Override AWS_PROFILE and configured credential profile
  --region REGION       Override configured region

No launch manifest/profile, valid default_ttl, SSH tools/keys, local receipts,
OpenTofu, SSM or scheduler health is required. AWS identity must match scope.
Missing expiry (legacy workers) and future deadlines are skipped. Malformed or
duplicate expiry is diagnosed; inspect with ls --json and use explicit down for
deliberate teardown. A dry run does not authorize a later stale candidate set.
Every cleanup rescans and rechecks exact IDs. Safe reruns observe concurrent or
previous termination; absent root mappings never prove root-volume deletion.
Exit 0: complete/no candidates; 1: failed; 2: invalid; 3: partial; 4: interrupted.
Scheduled deployment and laptop-offline acceptance must be verified separately.
`
