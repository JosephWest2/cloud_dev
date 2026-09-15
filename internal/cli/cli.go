package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/access"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/execution"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/logs"
	"os"
)

const help = `Usage: devbox [options] doctor
       devbox [options] up agent [--count N] [--group GROUP] [--name BASE] [--on-demand] [--ttl DURATION]
       devbox [options] up --resume REQUEST_ID
       devbox [options] up --retry-missing REQUEST_ID --after ATTEMPT_ID
       devbox [options] ls [--group GROUP]
       devbox [options] down NAME_OR_INSTANCE_ID [NAME_OR_INSTANCE_ID ...]
       devbox [options] down --group GROUP
       devbox [options] down --all [--yes]
       devbox [options] cleanup [--dry-run]
       devbox [options] ssh NAME_OR_INSTANCE_ID
       devbox [options] ssh-config NAME_OR_INSTANCE_ID
       devbox [options] proxy INSTANCE_ID
       devbox [options] exec NAME_OR_INSTANCE_ID [exec options] -- COMMAND [ARGS...]
       devbox [options] logs COMMAND_ID [logs options]
       devbox [--json] version

Options may appear before or after the command:
  --config PATH         User TOML (default: $XDG_CONFIG_HOME/devbox/config.toml
                        or ~/.config/devbox/config.toml)
  --aws-profile NAME    AWS profile (overrides AWS_PROFILE and user TOML)
  --region REGION       Explicit region (overrides user TOML)
  --timeout DURATION    Deadline (up/access/exec: 5m; cleanup: 165s; others: 20s; max: 5m)
  --name BASE           Base for stable names ending in each instance ID
  --count N             Instance count within configured max_count (default 10)
  --group GROUP         Launch group or exact inventory filter
  --ttl DURATION        Lifetime: default 2h, max 168h; active work never extends it
  --on-demand           Explicitly select On-Demand; no automatic fallback
  --resume REQUEST_ID   Observe a batch request without allocating
  --json                One versioned JSON result on stdout
  --help, -h            Show this help

Batch launch and recovery:
  up agent [--count N] [--group GROUP] [--name BASE] [--on-demand] [--ttl DURATION]
  up --retry-missing REQUEST_ID --after ATTEMPT_ID
  ls [--group GROUP]
  Count means instances; default 1, configured maximum default 10, range 1–100.
  Name defaults to the group, otherwise agent. Name/group labels are 1–63
  letters, digits, underscores or hyphens, starting with a letter or digit;
  labels must not look like an instance ID. Request/attempt IDs use 32 lowercase
  hex digits.
  Recovery cannot combine with launch parameters. --resume only reconciles
  batch receipts; --retry-missing explicitly requests proven missing capacity
  after the named attempt. These are separate from an SDK retry of one input.
  Launch preview lists eligible choices on stderr before allocation; each
  result reports actual placements. Readiness uses up to four concurrent
  workers under one overall deadline (default 5m). Resume never replaces
  interrupted or removed workers. Separate requests may share a group.

Expiry:
  up --ttl overrides config default_ttl, then the default 2h; maximum 168h.
  One deadline per request; active work and retries never extend it.
  Use unsigned Go durations (1h30m, 36h, 168h); no zero or unlimited.
  All launch overrides are rejected on resume/retry. Legacy requests cannot allocate.
  Scheduled cleanup is planned every 5m; deployment is verified separately.
  cleanup --dry-run previews expiry decisions; cleanup runs scoped expiry removal.
  Cleanup requires only trusted scope and AWS credentials (default deadline 165s).
  Missing/future expiry is skipped; malformed expiry is diagnosed. Use ls and
  explicit down for deliberate legacy teardown. Run cleanup --help for details.
  Scheduled deployment and laptop-offline acceptance remain separate gates.

Scoped teardown:
  down NAME_OR_INSTANCE_ID [NAME_OR_INSTANCE_ID ...]
  down --group GROUP
  down --all [--yes]
  Selectors are exclusive. --yes only applies to down --all; its exact scope
  and candidate IDs are always previewed on stderr. Without --yes, --all
  requires terminal confirmation. Piped input and EOF do not grant consent.
  Candidate IDs are frozen before confirmation and revalidated before removal.
  Independent valid targets can proceed when other selections fail. EC2
  termination and exact root-volume deletion are reported separately.

Exec options (all local options must precede --):
  --cwd PATH               Remote directory (default: /home/devbox)
  --exec-timeout DURATION   Remote runtime (default: 1h; whole seconds, 1s-24h)
  --delivery-timeout DURATION  SSM delivery (default: 5m; whole seconds, 30s-1h)
  --wait-timeout DURATION   Local result wait (default: 1h; positive, max: 25h)

Logs options:
  --stream stdout|stderr   Copy exact bytes to stdout; metadata goes to stderr
  --stdout-file PATH       Export stdout to a new file after checksum verification
  --stderr-file PATH       Export stderr to a distinct new file after verification
  Stream mode rejects --json and file exports. Exports may use --json.
  Default logs prints status; its exit code describes retrieval, not workload exit.

doctor checks local setup and AWS identity without provisioning resources.
up requires the expiry-capable version-6 foundation for instant Fleet allocation,
including explicit On-Demand and count one. Version-4/5 manifests and legacy
receipts remain usable for observation, down and saved logs. ls, individual access and down use AWS inventory without receipts.
up waits for EC2 + SSM + bootstrap readiness. ssh uses real SSH over SSM.
ssh-config supports editors/scp/sftp; proxy is its transport helper.
ssh/proxy reject --json. Failed/finished sessions retain workers: run down.
exec preserves every argument after -- and prints metadata, never workload bytes.
Ctrl-C detaches from exec; the remote command keeps running within its timeout.
logs uses retained cloud records and works after worker removal; no SSH is needed.
`

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, deps doctor.Dependencies) int {
	return RunWithLifecycle(ctx, args, stdout, stderr, deps, lifecycle.Dependencies{})
}

func RunWithLifecycle(ctx context.Context, args []string, stdout, stderr io.Writer, deps doctor.Dependencies, life lifecycle.Dependencies) int {
	return runWithExecution(ctx, args, stdout, stderr, deps, life, func(ctx context.Context, path string, overrides config.Overrides, options execution.RunOptions, diagnostics io.Writer) execution.Result {
		return execution.Run(ctx, path, overrides, options, execution.Dependencies{}, diagnostics)
	})
}

type execRunner func(context.Context, string, config.Overrides, execution.RunOptions, io.Writer) execution.Result
type logsRunner func(context.Context, string, config.Overrides, logs.Options, io.Writer) logs.Result

func runWithExecution(ctx context.Context, args []string, stdout, stderr io.Writer, deps doctor.Dependencies, life lifecycle.Dependencies, runExec execRunner) int {
	return runWithCommands(ctx, args, stdout, stderr, deps, life, runExec, func(ctx context.Context, path string, overrides config.Overrides, options logs.Options, output io.Writer) logs.Result {
		return logs.Run(ctx, path, overrides, options, logs.Dependencies{}, output)
	})
}

func runWithCommands(ctx context.Context, args []string, stdout, stderr io.Writer, deps doctor.Dependencies, life lifecycle.Dependencies, runExec execRunner, runLogs logsRunner) int {
	if isCleanupCommand(args) {
		return runCleanupCommand(ctx, args, stdout, stderr, nil)
	}
	jsonMode := false
	streamIntent := false
	// The first separator ends all local interpretation, including the early
	// error-format scan. Remote arguments never enable JSON or help locally.
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--json" {
			jsonMode = true
		}
		if a == "--stream" || strings.HasPrefix(a, "--stream=") {
			streamIntent = true
		}
	}
	var command, path string
	var logOptions logs.Options
	fail := func(message string) int {
		failureOutput := stdout
		if streamIntent && !jsonMode {
			failureOutput = stderr
		}
		if command == "exec" {
			return emitExecution(execution.Result{SchemaVersion: 1, Command: "exec", Outcome: "config_invalid", Code: "usage_invalid", Message: message, ExitCode: 2}, jsonMode, failureOutput, stderr)
		}
		if command == "logs" {
			return emitLogs(logs.Result{Result: execution.Result{SchemaVersion: 1, Command: "logs", Outcome: "config_invalid", Code: "usage_invalid", Message: message, ExitCode: 2}, Encoding: "bytes", Verification: "not_downloaded"}, jsonMode, streamIntent, stdout, stderr)
		}
		r := doctor.Result{SchemaVersion: 1, Command: "", OK: false, ExitCode: doctor.ExitConfig, Checks: []doctor.Check{{Name: "usage", Status: "fail", Code: "usage_invalid", Message: message}}}
		return emit(r, jsonMode, failureOutput, stderr)
	}
	var positional []string
	var launchFlags lifecycle.LaunchFlags
	var launch lifecycle.UpOptions
	var launchSelection *lifecycle.LaunchSelection
	var downSelection *lifecycle.DownSelection
	var group string
	all, yes := false, false
	lifecycleOptions := map[string]bool{}
	var overrides config.Overrides
	execOptions := execution.RunOptions{ExecTimeout: execution.DefaultExecTimeout, DeliveryTimeout: execution.DefaultDeliveryTimeout, WaitTimeout: execution.DefaultWaitTimeout}
	execOptionSet, separator := false, false
	logsOptionSet := false
	timeout := 20 * time.Second
	helpMode := false
	timeoutSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			separator = true
			execOptions.Argv = append([]string(nil), args[i+1:]...)
			break
		}
		if a == "--on-demand" || a == "--all" || a == "--yes" {
			if lifecycleOptions[a] {
				return fail("lifecycle options must not be repeated; run devbox --help")
			}
			lifecycleOptions[a] = true
			switch a {
			case "--on-demand":
				launchFlags.OnDemand = true
			case "--all":
				all = true
			case "--yes":
				yes = true
			}
			continue
		}
		if a == "--spot" {
			return fail("Spot is the default market; omit --spot, or explicitly select --on-demand")
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
		case "--config", "--aws-profile", "--region", "--timeout", "--name", "--count", "--group", "--resume", "--retry-missing", "--after", "--ttl", "--cwd", "--exec-timeout", "--delivery-timeout", "--wait-timeout", "--stream", "--stdout-file", "--stderr-file":
			if !hasVal {
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "--") {
					return fail("an option requires a value; run devbox --help")
				}
				val = args[i]
			}
			if val == "" && key != "--ttl" {
				return fail("option values must not be empty; run devbox --help")
			}
			switch key {
			case "--name", "--count", "--group", "--resume", "--retry-missing", "--after", "--ttl":
				if lifecycleOptions[key] {
					return fail("lifecycle options must not be repeated; run devbox --help")
				}
				lifecycleOptions[key] = true
			}
			switch key {
			case "--stream", "--stdout-file", "--stderr-file":
				logsOptionSet = true
				switch key {
				case "--stream":
					logOptions.Stream = val
				case "--stdout-file":
					logOptions.StdoutFile = val
				case "--stderr-file":
					logOptions.StderrFile = val
				}
			case "--cwd":
				execOptionSet = true
				execOptions.Cwd = val
			case "--exec-timeout", "--delivery-timeout", "--wait-timeout":
				execOptionSet = true
				duration, err := time.ParseDuration(val)
				if err != nil || duration <= 0 {
					return fail("exec timeouts must be positive durations; run devbox --help for bounds")
				}
				switch key {
				case "--exec-timeout":
					if duration > 24*time.Hour || duration%time.Second != 0 {
						return fail("--exec-timeout must be whole seconds from 1s through 24h")
					}
					execOptions.ExecTimeout = duration
				case "--delivery-timeout":
					if duration < 30*time.Second || duration > time.Hour || duration%time.Second != 0 {
						return fail("--delivery-timeout must be whole seconds from 30s through 1h")
					}
					execOptions.DeliveryTimeout = duration
				case "--wait-timeout":
					if duration > 25*time.Hour {
						return fail("--wait-timeout must be a positive duration through 25h")
					}
					execOptions.WaitTimeout = duration
				}
			case "--ttl":
				value := val
				launchFlags.TTL = &value
			case "--name":
				launchFlags.Name = val
			case "--count":
				launchFlags.Count = val
			case "--group":
				group = val
			case "--resume":
				launchFlags.Resume = val
			case "--retry-missing":
				launchFlags.RetryMissing = val
			case "--after":
				launchFlags.After = val
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
	if command != "exec" && (execOptionSet || separator) {
		return fail("exec options and the remote-argument separator require exec; run devbox --help")
	}
	if command != "logs" && logsOptionSet {
		return fail("output selection and export options require logs; run devbox --help")
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
	if command != "up" && (launchFlags.OnDemand || launchFlags.Name != "" || launchFlags.Count != "" || launchFlags.Resume != "" || launchFlags.RetryMissing != "" || launchFlags.After != "") {
		return fail("launch options require up; run devbox --help")
	}
	if group != "" && command != "up" && command != "ls" && command != "down" {
		return fail("--group requires up, ls or down; run devbox --help")
	}
	if (all || yes) && command != "down" {
		return fail("--all and --yes require down; run devbox --help")
	}
	isAccess := command == "ssh" || command == "ssh-config" || command == "proxy"
	if command != "up" && command != "down" && command != "exec" && command != "logs" && !isAccess && len(positional) > 0 {
		return fail("extra argument; run devbox --help")
	}
	if command == "logs" {
		if len(positional) != 1 || !execprotocol.ValidCommandID(positional[0]) {
			return fail("logs requires one public command ID: dc1- followed by 32 lowercase hexadecimal digits")
		}
		if logOptions.Stream != "" && (logOptions.Stream != "stdout" && logOptions.Stream != "stderr" || jsonMode || logOptions.StdoutFile != "" || logOptions.StderrFile != "") {
			return fail("--stream must select stdout or stderr and cannot combine with --json or file exports")
		}
	}
	if command == "exec" && (len(positional) != 1 || !lifecycle.ValidTarget(positional[0]) || !separator || len(execOptions.Argv) == 0 || execOptions.Argv[0] == "") {
		return fail("use exec with one managed name or instance ID, then -- COMMAND [ARGS...]")
	}
	if command != "up" && launchFlags.TTL != nil {
		return fail("--ttl requires up agent")
	}
	if command == "up" {
		launchFlags.Group = group
		selection, err := lifecycle.ResolveLaunchSelection(positional, launchFlags, config.HardMaxCount)
		if err != nil {
			var f *lifecycle.Failure
			if errors.As(err, &f) {
				return emitBatch(lifecycle.BatchResult{SchemaVersion: 2, Command: "up", ExitCode: 2, Code: f.Code, Message: f.Message}, "", jsonMode, stdout, stderr)
			}
			return fail(err.Error())
		}
		launch = lifecycle.UpOptions{Name: selection.Name, Resume: selection.Resume, OnDemand: selection.OnDemand}
		launchSelection = &selection
	}
	if isAccess && (len(positional) != 1 || !lifecycle.ValidTarget(positional[0])) {
		return fail("use ssh/ssh-config/proxy with one friendly name or instance ID")
	}
	if (command == "ssh" || command == "proxy") && jsonMode {
		return fail("interactive ssh and proxy reject --json; use ssh-config --json for separate configuration metadata")
	}
	if command == "down" {
		selection := lifecycle.DownSelection{Targets: positional, Group: group, All: all, Yes: yes}
		if err := lifecycle.ValidateDownSelection(selection); err != nil {
			return fail(err.Error())
		}
		downSelection = &selection
	}
	if command == "ls" && group != "" {
		if !lifecycle.ValidGroup(group) {
			return fail("--group requires a valid friendly label of 1–63 characters")
		}
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
	if command != "doctor" && command != "up" && command != "ls" && command != "down" && command != "exec" && command != "logs" && !isAccess {
		return fail("unknown or missing command; available commands: doctor, up, ls, down, cleanup, ssh, ssh-config, proxy, exec, logs, version; run devbox --help")
	}
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return fail(err.Error())
		}
	}

	if !timeoutSet && (command == "up" || command == "exec" || isAccess) {
		timeout = 5 * time.Minute
	}
	if command == "exec" {
		execOptions.Target, execOptions.SetupTimeout = positional[0], timeout
		return emitExecution(runExec(ctx, path, overrides, execOptions, stderr), jsonMode, stdout, stderr)
	}
	if command == "logs" {
		logOptions.CommandID, logOptions.Timeout = positional[0], timeout
		return emitLogs(runLogs(ctx, path, overrides, logOptions, stdout), jsonMode, logOptions.Stream != "", stdout, stderr)
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
		opts := lifecycle.Options{Command: command, UpOptions: launch, Selection: launchSelection, Group: group, DownSelection: downSelection}
		if command == "down" {
			if life.ConfirmDown == nil {
				life.ConfirmDown = newDownConfirmation(os.Stdin, stderr, downInputIsTerminal)
			}
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
	if r.Teardown != nil {
		return emitTeardown(*r.Teardown, r.RecoveryPrefix, jsonMode, stdout, stderr)
	}
	if r.Batch != nil {
		return emitBatch(*r.Batch, r.RecoveryPrefix, jsonMode, stdout, stderr)
	}
	if jsonMode {
		if err := json.NewEncoder(stdout).Encode(lifecycle.PublicResult(r)); err != nil {
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
			if _, err := fmt.Fprintf(stdout, "%s name=%q base=%q group=%q request=%q attempt=%s image=%s type=%s subnet=%s az=%s market=%s template=%s/%s ec2=%s ssm=%s bootstrap=%s readiness=%s root_deletion=%s expires_at=%s expiry_status=%s\n", i.ID, i.Name, i.BaseName, i.Group, i.RequestID, i.AttemptID, i.Image, i.Type, i.SubnetID, i.AvailabilityZone, i.Market, i.TemplateID, i.TemplateVersion, i.State, i.SSM, i.Bootstrap, i.Readiness, i.RootDeletion, expiryText(i.ExpiresAt), i.ExpiryStatus); err != nil {
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
			fmt.Fprintf(stderr, "devbox: retained instance %s; inspect: %s ls --json; cleanup: %s down %s --timeout 5m\n", i.ID, prefix, prefix, i.ID)
		}
		if r.RequestID != "" {
			if lifecycle.ValidRequest(r.RequestID) {
				fmt.Fprintf(stderr, "devbox: request %s; recover with %s up --resume %s\n", r.RequestID, prefix, r.RequestID)
			}
		}
	}
	return r.ExitCode
}
