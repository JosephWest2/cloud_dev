package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/identity"
)

// ConfirmDown must preview the exact frozen scope and IDs before returning
// consent. yes skips only the prompt. Missing callbacks never authorize --all.
type ConfirmDown func(context.Context, config.Config, []Instance, bool) (bool, error)

type TeardownResult struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	ExitCode      int    `json:"exit_code"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	TeardownOutcome
}

func runSelectedDown(ctx context.Context, path string, overrides config.Overrides, selection DownSelection, deps Dependencies) Result {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	r := TeardownResult{SchemaVersion: 2, Command: "down", TeardownOutcome: TeardownOutcome{Workers: []TeardownWorker{}, Errors: []ResourceError{}}}
	prefix := "devbox"
	finish := func(err error, usage bool) Result {
		if r.Workers == nil {
			r.Workers = []TeardownWorker{}
		}
		if r.Errors == nil {
			r.Errors = []ResourceError{}
		}
		r.OK, r.ExitCode = false, 1
		r.Code, r.Message = r.Status, "Cleanup is incomplete; inspect each returned instance and root volume, then retry down by exact ID."
		switch r.Status {
		case "teardown_complete":
			r.OK, r.ExitCode, r.Message = true, 0, "All selected instances are terminated and their captured disposable roots are verified deleted."
		case "no_managed_match":
			r.OK, r.ExitCode, r.Message = true, 0, "No managed targets matched; no particular termination or volume deletion was verified."
		case "confirmation_declined":
			r.OK, r.ExitCode, r.Message = true, 0, "Confirmation declined; no termination was requested."
		case "teardown_partial":
			r.ExitCode = ExitPartial
		case "":
			r.Status, r.Code = "teardown_failed", "teardown_failed"
		}
		if err != nil {
			if r.Status == "selected" {
				r.Status = "teardown_failed"
			}
			code, message := "service_unavailable", "AWS cleanup could not be completed; inspect returned identities and retry exact IDs."
			var f *Failure
			var id *identity.Failure
			if errors.As(err, &f) {
				code, message = f.Code, f.Message
			} else if errors.As(err, &id) {
				code, message = id.Code, id.Message
			}
			r.Errors = append(r.Errors, ResourceError{Code: code, Message: message})
			if r.OK || r.Status != "teardown_partial" {
				r.OK, r.ExitCode, r.Code, r.Message = false, 1, code, message
			}
			if code == "confirmation_required" || code == "usage_invalid" {
				usage = true
			}
			if code == "confirmation_required" {
				r.Status = "confirmation_required"
			}
		}
		if usage {
			r.OK, r.ExitCode = false, 2
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			r.OK, r.ExitCode, r.Code = false, 4, "operation_timeout"
			r.Message = "Teardown timed out or was canceled; keep the exact instance and root-volume IDs and retry inspection."
		}
		return Result{Teardown: &r, RecoveryPrefix: prefix, SchemaVersion: 2, Command: "down", OK: r.OK, ExitCode: r.ExitCode, Code: r.Code, Message: r.Message}
	}
	if err := ValidateDownSelection(selection); err != nil {
		return finish(failure("usage_invalid", err.Error()), true)
	}
	c, err := config.Load(path, overrides)
	if err != nil {
		return finish(failure("config_invalid", err.Error()), true)
	}
	prefix = config.CommandPrefix(path, c)
	factory := deps.New
	if factory == nil {
		factory = New
	}
	service, err := factory(ctx, c)
	if err != nil {
		return finish(err, false)
	}
	if service == nil {
		return finish(failure("service_unavailable", "AWS cleanup services are unavailable; no termination performed"), false)
	}
	plan, err := service.SelectDown(ctx, selection)
	r.TeardownOutcome = plan.Outcome()
	if err != nil {
		return finish(err, false)
	}
	if selection.All {
		if deps.ConfirmDown == nil {
			return finish(failure("confirmation_required", "down --all requires an exact candidate preview and explicit confirmation; use the CLI with --yes for noninteractive cleanup"), true)
		}
		approved, confirmErr := deps.ConfirmDown(ctx, c, plan.Candidates(), selection.Yes)
		if confirmErr != nil {
			return finish(confirmErr, false)
		}
		if err = ctx.Err(); err != nil {
			return finish(err, false)
		}
		if !approved {
			r.Status = "confirmation_declined"
			return finish(nil, false)
		}
	}
	r.TeardownOutcome, err = service.ExecuteDown(ctx, plan)
	return finish(err, false)
}
