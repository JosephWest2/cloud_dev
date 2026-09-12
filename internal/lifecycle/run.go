package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/identity"
)

type Options struct {
	Command, Target string
	UpOptions
}
type Dependencies struct {
	New   func(context.Context, config.Config) (*Service, error)
	Store *Store
}
type Result struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	ExitCode      int    `json:"exit_code"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	Outcome
}

func Run(ctx context.Context, path string, overrides config.Overrides, o Options, deps Dependencies, diagnostics io.Writer) Result {
	r := Result{SchemaVersion: 1, Command: o.Command, OK: true, Outcome: Outcome{Instances: []Instance{}}}
	fail := func(err error, exit int) Result {
		r.OK = false
		r.ExitCode = exit
		r.Code = "service_unavailable"
		r.Message = "AWS operation failed; check credentials, permissions and connectivity; inspect returned identities before retrying"
		var f *Failure
		var id *identity.Failure
		if errors.As(err, &f) {
			r.Code = f.Code
			r.Message = f.Message
		} else if errors.As(err, &id) {
			r.Code = id.Code
			r.Message = id.Message
			if id.Code == "timeout" {
				r.ExitCode = 4
			}
		}
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			r.ExitCode = 4
			r.Code = "operation_timeout"
			r.Message = "operation timed out or was canceled; inspect returned instance/request/volume IDs; resume an existing request or retry down by ID with --timeout 5m"
		}
		return r
	}
	c, err := config.Load(path, overrides)
	if err != nil {
		return fail(failure("config_invalid", err.Error()), 2)
	}
	var m config.Manifest
	var p config.Profile
	var store Store
	if o.Command == "up" {
		if o.Resume == "" && !o.OnDemand {
			return fail(failure("spot_unsupported", "Spot is unsupported; launch explicitly with devbox up agent --on-demand --name NAME"), 2)
		}
		if deps.Store != nil {
			store = *deps.Store
		} else {
			store, err = DefaultStore()
			if err != nil {
				return fail(err, 1)
			}
		}
		needsLaunch := true
		if o.Resume != "" {
			receipt, loadErr := store.Load(o.Resume)
			if loadErr != nil {
				return fail(loadErr, 2)
			}
			r.RequestID = receipt.RequestID
			r.ReceiptPath = store.Path(receipt.RequestID)
			needsLaunch = receipt.State == "prepared"
			for _, id := range receipt.InstanceIDs {
				r.Instances = append(r.Instances, Instance{ID: id, State: "not_observed", SSM: "not_observed", Bootstrap: "not_observed", Readiness: "not_observed", RootDeletion: "unavailable", Volumes: []Volume{}})
			}
		}
		if needsLaunch {
			p, err = config.LoadProfile(c.ProfileFile)
			if err != nil {
				return fail(failure("profile_invalid", err.Error()), 2)
			}
			m, err = config.LoadManifest(c.Manifest, c, p)
			if err != nil {
				return fail(failure("manifest_invalid", err.Error()), 1)
			}
		}
	}
	factory := deps.New
	if factory == nil {
		factory = New
	}
	service, err := factory(ctx, c)
	if err != nil {
		return fail(err, 1)
	}
	switch o.Command {
	case "ls":
		r.Instances, err = service.List(ctx)
		r.Status = "inventory"
	case "down":
		r.Outcome, err = service.Down(ctx, o.Target)
	case "up":
		r.Outcome, err = service.Up(ctx, m, p, o.UpOptions, store, func(receipt Receipt, path string) error {
			_, err := fmt.Fprintf(diagnostics, "devbox: saved request %s at %q; resume with: devbox up --resume %s (use the same config and AWS scope)\n", receipt.RequestID, path, receipt.RequestID)
			return err
		})
	default:
		return fail(failure("usage_invalid", "unknown lifecycle command"), 2)
	}
	if err != nil {
		return fail(err, 1)
	}
	r.Code = r.Status
	switch r.Status {
	case "allocated":
		r.Message = "allocation observed; EC2 state is reported separately; SSM/bootstrap readiness has not been observed"
	case "inventory":
		r.Message = "AWS-authoritative managed inventory; duplicate names require explicit instance IDs"
	case "no_managed_match":
		r.Message = "no managed match in the selected scope; no specific termination or volume deletion was verified"
	case "already_terminated":
		r.Message = "AWS reports already-terminated instances; consult root_volume_deletion for separate deletion evidence"
	case "terminated":
		r.Message = "EC2 termination observed; consult root_volume_deletion for separate deletion evidence"
	case "shutting_down":
		r.Message = "the request's instance is shutting down; use down by ID to observe cleanup"
	}
	return r
}
