package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/identity"
)

// runSelectedUp keeps legacy receipts on their original recovery path. New v5
// launches and shared recovery use one schema-v2 result, including early errors.
func runSelectedUp(ctx context.Context, path string, overrides config.Overrides, o Options, deps Dependencies, diagnostics io.Writer) Result {
	r := BatchResult{SchemaVersion: 2, Command: "up", BatchOutcome: BatchOutcome{Status: "prepared", Workers: []WorkerOutcome{}, Attempts: []AttemptOutcome{}, Errors: []ResourceError{}}}
	prefix := "devbox"
	finish := func(err error, usage bool) Result {
		if r.Workers == nil {
			r.Workers = []WorkerOutcome{}
		}
		if r.Attempts == nil {
			r.Attempts = []AttemptOutcome{}
		}
		if r.Errors == nil {
			r.Errors = []ResourceError{}
		}
		if r.ResumeCommand == "" && ValidRequest(r.RequestID) {
			r.ResumeCommand = prefix + " up --resume " + r.RequestID
		}
		completeBatchResult(ctx, &r, err, usage)
		return Result{Batch: &r, RecoveryPrefix: prefix, SchemaVersion: 2, Command: "up", OK: r.OK, ExitCode: r.ExitCode, Code: r.Code, Message: r.Message}
	}
	c, err := config.Load(path, overrides)
	if err != nil {
		return finish(failure("config_invalid", err.Error()), true)
	}
	prefix = config.CommandPrefix(path, c)
	selection := *o.Selection
	for _, id := range []string{selection.Resume, selection.RetryMissing} {
		if ValidRequest(id) {
			r.RequestID, r.Status = id, "allocation_unknown"
			r.ResumeCommand = prefix + " up --resume " + id
		}
	}
	if selection.Resume == "" && selection.RetryMissing == "" && (selection.Count < 1 || selection.Count > c.MaxCount) {
		return finish(failure("count_invalid", "requested instance count exceeds configured max_count; no allocation performed"), true)
	}
	var store Store
	if deps.Store != nil {
		store = *deps.Store
	} else if store, err = DefaultStore(); err != nil {
		return finish(err, false)
	}
	legacy := func() Result {
		o.Selection = nil
		o.UpOptions = UpOptions{Name: selection.Name, Resume: selection.Resume, OnDemand: selection.OnDemand}
		return Run(ctx, path, overrides, o, deps, diagnostics)
	}
	if selection.Resume != "" {
		r.RequestID = selection.Resume
		if _, loadErr := store.Load(selection.Resume); loadErr == nil {
			return legacy()
		}
	} else {
		r.RequestID = selection.RetryMissing
	}
	// Observation has no dependency on a mutable local launch profile. The
	// original plan supplies its readiness pins after shared validation.
	p := config.Profile{Image: "agent"}
	if selection.Resume == "" {
		p, err = config.LoadProfile(c.ProfileFile)
		if err != nil {
			return finish(failure("profile_invalid", err.Error()), true)
		}
	}
	m, err := config.LoadManifest(c.Manifest, c, p)
	if err != nil {
		return finish(failure("manifest_invalid", err.Error()), false)
	}
	if m.SchemaVersion != 5 {
		if selection.Resume != "" || selection.RetryMissing == "" && selection.OnDemand && selection.Count == 1 && selection.Group == "" {
			return legacy()
		}
		return finish(failure("manifest_upgrade_required", "Spot, batch and shared recovery require the version-5 foundation; apply and re-export it before launching"), true)
	}
	var prepared BatchReceipt
	if selection.Resume == "" && selection.RetryMissing == "" {
		prepared, err = NewBatchReceipt(c, m, p, selection)
		if err != nil {
			return finish(err, true)
		}
		r.RequestID, r.Plan, r.RequestedCount = prepared.RequestID, prepared.Plan, prepared.Plan.RequestedCount
	}
	factory := deps.New
	if factory == nil {
		factory = New
	}
	service, err := factory(ctx, c)
	if err != nil {
		return finish(err, false)
	}
	if service == nil {
		return finish(failure("service_unavailable", "AWS services are unavailable; no allocation performed"), false)
	}
	ledger, err := NewS3LaunchLedger(service.LaunchRecords, c, *m.LaunchLedger)
	if err != nil {
		return finish(err, false)
	}
	inventory, ok := service.API.(BatchInventory)
	if !ok {
		return finish(failure("recovery_unavailable", "exact worker and Fleet inspection is required before batch operations"), false)
	}
	dispatcher := &AttemptService{API: service.Fleet, Scope: c, Ledger: ledger, Cache: store, VerifyFoundation: service.VerifyFoundation,
		VerifyWorkers: func(ctx context.Context, plan LaunchPlan, attempt AttemptReceipt, workers []WorkerOutcome) ([]WorkerOutcome, error) {
			return VerifyFleetWorkers(ctx, inventory, plan, attempt, workers)
		}}
	recovery := &RecoveryService{Scope: c, Ledger: ledger, Cache: store, Dispatcher: dispatcher, CommandPrefix: prefix,
		Observe: func(ctx context.Context, snapshot LaunchSnapshot) (LaunchObservation, error) {
			return ReconcileLaunch(ctx, inventory, snapshot)
		}}
	announce := func(receipt BatchReceipt, cachePath string) error {
		if diagnostics == nil {
			return failure("output_unavailable", "launch preview requires an output destination")
		}
		if err := writeLaunchPreview(diagnostics, receipt); err != nil {
			return err
		}
		_, err := fmt.Fprintf(diagnostics, "devbox: saved request %s at %q; recover with: %s up --resume %s\n", receipt.RequestID, cachePath, prefix, receipt.RequestID)
		return err
	}
	switch {
	case selection.Resume != "":
		r.BatchOutcome, err = recovery.Resume(ctx, selection.Resume)
	case selection.RetryMissing != "":
		r.BatchOutcome, err = recovery.RetryMissing(ctx, m, p, selection.RetryMissing, selection.After, announce)
	default:
		var dispatched AttemptResult
		dispatched, err = dispatcher.Dispatch(ctx, m, p, prepared, announce)
		r.BatchOutcome = recovery.outcome(LaunchObservation{Receipt: dispatched.Receipt, Workers: dispatched.Workers,
			Bounded: err == nil && dispatched.Outcome().MissingCount != nil})
	}
	// Readiness cannot change capacity evidence or allocate replacements. Keep
	// observing independently verified peers even if allocation was partial.
	if len(r.Workers) > 0 && r.Plan.SchemaVersion == 1 {
		pinned := config.Manifest{Readiness: r.Plan.Readiness}
		readyErr := service.WaitBatchReady(ctx, pinned, r.Workers, diagnostics)
		err = errors.Join(err, readyErr)
	}
	return finish(err, false)
}

func writeLaunchPreview(w io.Writer, receipt BatchReceipt) error {
	p := receipt.Plan
	types := map[string]bool{}
	places := map[string]bool{}
	for _, choice := range p.Choices {
		types[choice.InstanceType] = true
		places[choice.SubnetID+"/"+choice.AvailabilityZone] = true
	}
	keys := func(set map[string]bool) []string {
		values := make([]string, 0, len(set))
		for value := range set {
			values = append(values, value)
		}
		sort.Strings(values)
		return values
	}
	count := p.RequestedCount
	if len(receipt.Attempts) > 0 {
		count = receipt.Attempts[len(receipt.Attempts)-1].RequestedCount
	}
	_, err := fmt.Fprintf(w, "devbox: launch profile=%s region=%s market=%s count=%d original_count=%d base=%q group=%q eligible_types=%s eligible_subnets_azs=%s\n", p.Profile, p.Region, p.Market, count, p.RequestedCount, p.BaseName, p.Group, strings.Join(keys(types), ","), strings.Join(keys(places), ","))
	return err
}

func completeBatchResult(ctx context.Context, r *BatchResult, err error, usage bool) {
	r.ReadyCount = 0
	for _, worker := range r.Workers {
		if worker.Readiness == "ready" {
			r.ReadyCount++
		}
	}
	r.OK, r.ExitCode = false, 1
	r.Code, r.Message = "allocation_unknown", "Allocation is unresolved; resume this request and inspect known IDs without requesting more capacity."
	switch {
	case r.Status == "prepared":
		r.Code, r.Message = "prepared", "Request prepared; resume only observes it. No confirmed allocation is available."
	case r.MissingCount == nil:
		r.Status = "allocation_unknown"
	case *r.MissingCount > 0 && r.FulfilledCount > 0:
		r.Status, r.Code, r.ExitCode = "partial_capacity", "partial_capacity", ExitPartial
		r.Message = "Partial capacity; successful workers are retained. Use the explicit missing-capacity retry command for the original remainder."
	case *r.MissingCount > 0:
		r.Status, r.Code = "no_capacity", "no_capacity"
		r.Message = "No capacity was allocated. Retry the proven remainder with the same pool, or deliberately create a new request after changing the profile; markets never switch automatically."
	case r.ReadyCount == r.RequestedCount && r.ReadyCount > 0:
		r.Status, r.Code, r.Message, r.OK, r.ExitCode = "ready", "ready", "All original workers are ready; use each returned name or instance ID for ssh and exec.", true, 0
	default:
		r.Status, r.Code = "readiness_failed", "readiness_failed"
		r.Message = "Allocation is complete, but some workers are not ready; inspect each worker and resume observation without allocating replacements."
		if r.ReadyCount > 0 {
			r.ExitCode = ExitPartial
		}
	}
	if err != nil {
		code, message := "service_unavailable", "AWS observation or persistence failed; preserve every returned identity and resume without allocating again."
		var f *Failure
		var id *identity.Failure
		if errors.As(err, &f) {
			code, message = f.Code, f.Message
			usage = usage || f.Code == "count_invalid"
		} else if errors.As(err, &id) {
			code, message = id.Code, id.Message
		}
		r.Errors = append(r.Errors, ResourceError{Code: code, Message: message})
		if r.OK || r.Status == "prepared" || r.Status == "allocation_unknown" || usage {
			r.Code, r.Message, r.OK, r.ExitCode = code, message, false, 1
		}
	}
	if usage {
		r.OK, r.ExitCode = false, 2
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.OK, r.ExitCode, r.Code = false, 4, "operation_timeout"
		r.Message = "Operation timed out or was canceled; preserve the returned request, worker and volume IDs and resume observation."
	}
}
