package execution

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
)

const (
	DefaultSetupTimeout    = 5 * time.Minute
	DefaultWaitTimeout     = time.Hour
	DefaultDeliveryTimeout = 5 * time.Minute
	DefaultExecTimeout     = time.Hour
)

type RunOptions struct {
	Target                                                  string
	Argv                                                    []string
	Cwd                                                     string
	SetupTimeout, WaitTimeout, DeliveryTimeout, ExecTimeout time.Duration
}

type Dependencies struct {
	New func(context.Context, config.Config, config.Manifest) (*Service, error)
}

// Result separates the workload's status from setup, observation and publication.
// Workload bytes are retrieved explicitly through logs, never placed in this envelope.
type Result struct {
	SchemaVersion   int                    `json:"schema_version"`
	Command         string                 `json:"command"`
	OK              bool                   `json:"ok"`
	ExitCode        int                    `json:"exit_code"`
	Outcome         string                 `json:"outcome"`
	Code            string                 `json:"code,omitempty"`
	Message         string                 `json:"message"`
	CommandID       string                 `json:"command_id,omitempty"`
	SSMCommandID    string                 `json:"ssm_command_id,omitempty"`
	InstanceID      string                 `json:"instance_id,omitempty"`
	SubmissionState string                 `json:"submission_state,omitempty"`
	RecoveryCommand string                 `json:"recovery_command,omitempty"`
	Workload        *execprotocol.Workload `json:"workload,omitempty"`
	Capture         string                 `json:"capture,omitempty"`
	Publication     string                 `json:"publication,omitempty"`
	Streams         *execprotocol.Streams  `json:"streams,omitempty"`
	Warnings        []string               `json:"warnings,omitempty"`
	SSM             *SSMObservation        `json:"ssm,omitempty"`
	DurableState    DurableState           `json:"durable_state,omitempty"`
	SubmittedAt     string                 `json:"submitted_at,omitempty"`
	ExpiresAt       string                 `json:"expires_at,omitempty"`
	StartedAt       string                 `json:"started_at,omitempty"`
	FinishedAt      string                 `json:"finished_at,omitempty"`
}

func Run(ctx context.Context, path string, overrides config.Overrides, o RunOptions, deps Dependencies, diagnostics io.Writer) Result {
	r := Result{SchemaVersion: 1, Command: "exec"}
	badConfig := func(message string) Result {
		r.Outcome, r.Code, r.Message, r.ExitCode = "config_invalid", "config_invalid", message, 2
		return r
	}
	if o.SetupTimeout == 0 {
		o.SetupTimeout = DefaultSetupTimeout
	}
	if o.WaitTimeout == 0 {
		o.WaitTimeout = DefaultWaitTimeout
	}
	if o.DeliveryTimeout == 0 {
		o.DeliveryTimeout = DefaultDeliveryTimeout
	}
	if o.ExecTimeout == 0 {
		o.ExecTimeout = DefaultExecTimeout
	}
	if !lifecycle.ValidTarget(o.Target) {
		return badConfig("exec requires one managed name or instance ID")
	}
	if o.SetupTimeout <= 0 || o.SetupTimeout > 5*time.Minute || o.WaitTimeout <= 0 || o.WaitTimeout > 25*time.Hour || o.DeliveryTimeout < 30*time.Second || o.DeliveryTimeout > time.Hour || o.DeliveryTimeout%time.Second != 0 {
		return badConfig("invalid exec timing options; run devbox --help for the independent setup, delivery, execution and wait bounds")
	}
	payload, err := Prepare(o.Argv, o.Cwd, o.ExecTimeout)
	if err != nil {
		return badConfig("invalid remote arguments, working directory or execution timeout; use valid UTF-8 without NUL, at most 256 arguments and a whole-second timeout from 1s through 24h")
	}
	setup, cancelSetup := context.WithTimeout(ctx, o.SetupTimeout)
	defer cancelSetup()
	fail := func(err error) Result {
		r.Outcome, r.Code, r.Message, r.ExitCode = "setup_failed", "service_unavailable", "command setup failed; check the selected AWS profile, permissions and connectivity", 1
		var f *Failure
		var id *identity.Failure
		var life *lifecycle.Failure
		if errors.As(err, &f) {
			r.Code, r.Message = f.Code, f.Message
			if f.Code == "submission_unknown" {
				r.Outcome = "submission_unknown"
			}
		} else if errors.As(err, &id) {
			r.Code, r.Message = id.Code, id.Message
			if id.Code == "timeout" {
				r.Outcome, r.ExitCode = "setup_timeout", 4
			}
		} else if errors.As(err, &life) {
			r.Code, r.Message = life.Code, life.Message
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			r.Outcome, r.Code, r.Message, r.ExitCode = "interrupted", "interrupted", "local command setup stopped; any submitted remote command may continue; recover using its command ID", 4
		} else if r.Outcome != "submission_unknown" && (errors.Is(setup.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)) {
			r.Outcome, r.Code, r.Message, r.ExitCode = "setup_timeout", "setup_timeout", "local setup deadline expired; inspect the retained command ID before submitting another command", 4
		}
		return r
	}
	c, err := config.Load(path, overrides)
	if err != nil {
		return badConfig(err.Error())
	}
	m, err := config.LoadExecutionManifest(c.Manifest, c)
	if err != nil {
		r.Outcome, r.Code, r.Message, r.ExitCode = "setup_failed", "manifest_invalid", "execution requires the matching manifest v4 with pinned runtime and storage resources; apply and re-export the foundation", 1
		return r
	}
	if err = setup.Err(); err != nil {
		return fail(err)
	}
	factory := deps.New
	if factory == nil {
		factory = New
	}
	service, err := factory(setup, c, m)
	if err != nil {
		return fail(err)
	}
	if service == nil || service.Store == nil {
		return fail(errors.New("missing execution service"))
	}
	sub, err := service.Submit(setup, o.Target, payload, int(o.DeliveryTimeout/time.Second), diagnostics)
	r.CommandID, r.SSMCommandID, r.InstanceID = sub.Binding.CommandID, sub.SSMCommandID, sub.Binding.InstanceID
	r.SubmissionState = sub.State
	if r.CommandID != "" {
		r.RecoveryCommand = config.CommandPrefix(path, c) + " logs " + r.CommandID
	}
	if err != nil {
		return fail(err)
	}
	cancelSetup()
	wait, cancelWait := context.WithTimeout(ctx, o.WaitTimeout)
	defer cancelWait()
	completed := Observe(wait, service.Store, service.SSM, sub, ObserveOptions{})
	completed.SubmissionState = sub.State
	completed.RecoveryCommand = r.RecoveryCommand
	if sub.DiagnosticCode != "" {
		completed.Warnings = append(completed.Warnings, sub.DiagnosticCode)
	}
	return completed
}
