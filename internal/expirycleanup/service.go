// Package expirycleanup implements the shared, launch-independent AWS expiry
// cleanup service. Injected clients, clocks, waiters and sinks must be safe for
// concurrent use and honor context cancellation. NewAWS binds production clients
// to one explicit region; New supports controlled dependency injection.
package expirycleanup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type EC2 interface {
	Options() ec2.Options
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
}
type STS interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// WaitFunc must return promptly on cancellation. Nil uses a context-aware timer.
type WaitFunc func(context.Context, time.Duration) error
type Dependencies struct {
	EC2   EC2
	STS   STS
	Clock expiry.Clock
	Sink  expiry.Sink
	Wait  WaitFunc
}

// Zero fields select defaults. Positive overrides may only tighten the contract.
// ReadAttempts includes the initial SDK attempt, not additional retries.
type Limits struct {
	RequestTimeout      time.Duration
	InvocationTimeout   time.Duration
	MaxPages            int
	ObservationAttempts int
	Concurrency         int
	ReadAttempts        int
	PollInterval        time.Duration
}

func (l Limits) normalized() (Limits, error) {
	ds := []*time.Duration{&l.RequestTimeout, &l.InvocationTimeout, &l.PollInterval}
	defaults := []time.Duration{15 * time.Second, 165 * time.Second, 2 * time.Second}
	for i, p := range ds {
		if *p == 0 {
			*p = defaults[i]
		}
		if *p < 0 || *p > defaults[i] {
			return l, failure("cleanup_invalid")
		}
	}
	ns := []*int{&l.MaxPages, &l.ObservationAttempts, &l.Concurrency, &l.ReadAttempts}
	maxima := []int{128, 30, 4, 3}
	for i, p := range ns {
		if *p == 0 {
			*p = maxima[i]
		}
		if *p < 1 || *p > maxima[i] {
			return l, failure("cleanup_invalid")
		}
	}
	return l, nil
}

type Service struct {
	scope  expiry.Scope
	deps   Dependencies
	limits Limits
}

// New requires an explicit region-bound EC2 client and an acknowledged sink.
// Dependencies are trusted application wiring, never scheduled input. Each Run
// independently verifies STS before discovery and owns its frozen authority.
func New(scope expiry.Scope, deps Dependencies, limits Limits) (*Service, error) {
	if scope.Validate() != nil || isNil(deps.EC2) || isNil(deps.STS) || isNil(deps.Clock) || isNil(deps.Sink) {
		return nil, failure("cleanup_invalid")
	}
	if deps.EC2.Options().Region != scope.Region {
		return nil, failure("cleanup_invalid")
	}
	l, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if deps.Wait == nil {
		deps.Wait = wait
	}
	return &Service{scope: scope, deps: deps, limits: l}, nil
}
func isNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Failure exposes only stable diagnostics, never raw AWS/provider errors.
type Failure struct{ Code string }

func (f *Failure) Error() string { return f.Code }
func failure(code string) error  { return &Failure{Code: code} }
func problem(id, code string) expiry.Problem {
	return expiry.Problem{ResourceID: id, Code: code, Message: code}
}
func addProblem(o *expiry.Outcome, code string) {
	o.Errors = append(o.Errors, problem(o.InstanceID, code))
}
func benign(reason expiry.Reason) bool {
	switch reason {
	case expiry.Expired, expiry.ExpiryFuture, expiry.ExpiryMissing, expiry.ExpiryChanged, expiry.AlreadyTerminated, expiry.AlreadyTerminating:
		return true
	}
	return false
}

type worker struct {
	selected   expiry.Decision
	record     record
	outcome    expiry.Outcome
	active     bool
	terminal   bool
	nextVolume int
}

// Run performs a new scan even for repeated/concurrent calls. Dry-run returns
// advisory decisions without calling the sink or any mutation API.
func (s *Service) Run(parent context.Context, dryRun bool) (expiry.Result, error) {
	ctx, cancel := context.WithTimeout(parent, s.limits.InvocationTimeout)
	defer cancel()
	r := expiry.Result{SchemaVersion: 1, Command: "cleanup", Scope: s.scope, DryRun: dryRun, Instances: []expiry.Outcome{}, Errors: []expiry.Problem{}}
	runBytes := make([]byte, 16)
	if _, err := rand.Read(runBytes); err != nil {
		r.Errors = append(r.Errors, problem("", "evidence_unavailable"))
		return s.finish(ctx, "", r, nil)
	}
	runID := hex.EncodeToString(runBytes)
	now := s.deps.Clock.Now()
	var err error
	r.EvaluatedAt, err = expiry.Timestamp(now)
	if err != nil {
		r.Errors = append(r.Errors, problem("", "clock_invalid"))
		return s.finish(ctx, runID, r, nil)
	}
	if err = s.verify(ctx); err != nil {
		r.Errors = append(r.Errors, problem("", "identity_unverified"))
		return s.finish(ctx, runID, r, nil)
	}
	records, complete := s.scan(ctx, "")
	r.ScanComplete = complete
	if !complete {
		r.Errors = append(r.Errors, problem("", "scan_incomplete"))
	}
	workers := make([]worker, 0, len(records))
	for _, rec := range records {
		d := expiry.Evaluate(s.scope, rec.resource, now)
		if rec.conflict {
			d.Eligible = false
			d.Reason = expiry.ResourceInvalid
		}
		o := expiry.Outcome{Decision: cloneDecision(d), Status: "skipped", RootDeletion: "not_observed", Volumes: cloneVolumes(rec.volumes), Errors: []expiry.Problem{}}
		if d.Reason == expiry.AlreadyTerminated {
			o.Status = "already_terminated"
			if historicalMappingsAbsent(rec, d.Reason) {
				o.RootDeletion = "unavailable"
			}
		}
		if d.Reason == expiry.AlreadyTerminating {
			o.Status = "already_terminating"
		}
		if !benign(d.Reason) {
			addProblem(&o, string(d.Reason))
		}
		if instanceID.MatchString(d.InstanceID) {
			r.ScannedCount++
		}
		if complete && d.Eligible {
			r.CandidateCount++
			o.Status = "termination_not_requested"
		}
		workers = append(workers, worker{selected: cloneDecision(d), record: rec, outcome: o})
	}
	// All IDs and decisions exist before any sink call or execution slot is used.
	s.parallel(ctx, len(workers), func(i int) {
		w := &workers[i]
		if !dryRun {
			if err := s.emit(ctx, runID, "decision", &w.outcome, nil); err != nil {
				addProblem(&w.outcome, "evidence_unavailable")
				return
			}
		}
		if !complete {
			return
		}
		if dryRun {
			if historicalMappingsAbsent(w.record, w.selected.Reason) {
				return
			}
			if w.selected.Eligible || w.selected.Reason == expiry.AlreadyTerminated || w.selected.Reason == expiry.AlreadyTerminating {
				if code := rootProblem(w.record); code != "" {
					addProblem(&w.outcome, code)
					setRootDiagnostic(&w.outcome, code)
				} else if w.selected.Eligible {
					w.outcome.Status = "would_terminate"
				}
			}
			return
		}
		if w.selected.Eligible || w.selected.Reason == expiry.AlreadyTerminated || w.selected.Reason == expiry.AlreadyTerminating {
			s.prepare(ctx, runID, w)
		}
	})
	if !dryRun {
		// A round gives each peer a bounded observation turn; a slow resource never
		// consumes all 30 attempts ahead of the next queued peer.
		for attempt := 0; attempt < s.limits.ObservationAttempts && ctx.Err() == nil; attempt++ {
			active := false
			for i := range workers {
				active = active || workers[i].active
			}
			if !active {
				break
			}
			s.parallel(ctx, len(workers), func(i int) {
				if workers[i].active {
					s.observe(ctx, &workers[i])
				}
			})
			active = false
			for i := range workers {
				active = active || workers[i].active
			}
			if !active {
				break
			}
			if attempt+1 < s.limits.ObservationAttempts {
				if err := s.deps.Wait(ctx, s.limits.PollInterval); err != nil {
					r.Errors = append(r.Errors, problem("", "interrupted"))
					break
				}
			}
		}
	}
	for i := range workers {
		w := &workers[i]
		if w.active {
			if !w.terminal {
				addProblem(&w.outcome, "termination_unresolved")
			} else {
				unresolvedVolumes(&w.outcome)
			}
		}
		if ctx.Err() != nil && w.outcome.Status == "termination_not_requested" {
			addProblem(&w.outcome, "interrupted")
		}
	}
	if !dryRun {
		s.parallel(ctx, len(workers), func(i int) {
			if err := s.emit(ctx, runID, "outcome", &workers[i].outcome, nil); err != nil {
				addProblem(&workers[i].outcome, "evidence_unavailable")
			}
		})
	}
	return s.finish(ctx, runID, r, workers)
}
func (s *Service) parallel(ctx context.Context, n int, fn func(int)) {
	var wg sync.WaitGroup
	jobs := make(chan int)
	for k := 0; k < s.limits.Concurrency; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
}
func (s *Service) finish(ctx context.Context, runID string, r expiry.Result, workers []worker) (expiry.Result, error) {
	useful := 0
	for _, w := range workers {
		r.Instances = append(r.Instances, cloneOutcome(w.outcome))
		if !r.DryRun && w.terminal {
			r.TerminatedCount++
			if w.outcome.RootDeletion == "deleted" {
				r.CleanedCount++
				useful++
			}
		}
		if r.DryRun && w.outcome.Status == "would_terminate" {
			useful++
		}
	}
	sort.SliceStable(r.Instances, func(i, j int) bool { return r.Instances[i].InstanceID < r.Instances[j].InstanceID })
	r.CompletedAt, _ = expiry.Timestamp(s.deps.Clock.Now())
	if r.CompletedAt == "" {
		r.Errors = append(r.Errors, problem("", "clock_invalid"))
	}
	aggregate(ctx, &r, useful)
	if runID != "" && !r.DryRun {
		if err := s.emit(ctx, runID, "summary", nil, &r); err != nil {
			r.Errors = append(r.Errors, problem("", "evidence_unavailable"))
			aggregate(ctx, &r, useful)
		}
	}
	if !r.OK {
		return r, failure(r.Code)
	}
	return r, nil
}
func aggregate(ctx context.Context, r *expiry.Result, useful int) {
	bad := !r.ScanComplete || len(r.Errors) > 0
	interrupted := ctx.Err() != nil
	for _, p := range r.Errors {
		interrupted = interrupted || p.Code == "interrupted"
	}
	for _, o := range r.Instances {
		bad = bad || len(o.Errors) > 0
	}
	r.Complete = !bad && !interrupted
	r.OK = r.Complete
	r.ExitCode = 0
	r.Code = "cleanup_complete"
	if r.CandidateCount == 0 && useful == 0 {
		r.Code = "cleanup_no_candidates"
	}
	if bad {
		r.ExitCode = 1
		r.Code = "cleanup_failed"
		if useful > 0 {
			r.ExitCode = 3
			r.Code = "cleanup_partial"
		}
	}
	if interrupted {
		r.ExitCode = 4
		r.Code = "cleanup_interrupted"
	}
	r.Message = r.Code
}
func cloneDecision(d expiry.Decision) expiry.Decision {
	if d.ExpiresAt != nil {
		v := *d.ExpiresAt
		d.ExpiresAt = &v
	}
	return d
}
func cloneVolumes(vs []expiry.Volume) []expiry.Volume {
	out := make([]expiry.Volume, len(vs))
	copy(out, vs)
	for i := range out {
		if out[i].DeleteOnTermination != nil {
			v := *out[i].DeleteOnTermination
			out[i].DeleteOnTermination = &v
		}
	}
	return out
}
func cloneOutcome(o expiry.Outcome) expiry.Outcome {
	o.Decision = cloneDecision(o.Decision)
	o.Volumes = cloneVolumes(o.Volumes)
	o.Errors = append([]expiry.Problem{}, o.Errors...)
	return o
}
func (s *Service) emit(ctx context.Context, runID, kind string, o *expiry.Outcome, r *expiry.Result) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	timestamp, err := expiry.Timestamp(s.deps.Clock.Now())
	if err != nil {
		return err
	}
	e := expiry.Event{SchemaVersion: 1, RunID: runID, Kind: kind, Scope: s.scope, EmittedAt: timestamp}
	if o != nil {
		copy := cloneOutcome(*o)
		e.Instance = &copy
	}
	if r != nil {
		copy := *r
		copy.Errors = append([]expiry.Problem{}, r.Errors...)
		copy.Instances = make([]expiry.Outcome, len(r.Instances))
		for i := range r.Instances {
			copy.Instances[i] = cloneOutcome(r.Instances[i])
		}
		e.Summary = &copy
	}
	request, cancel := context.WithTimeout(ctx, s.limits.RequestTimeout)
	defer cancel()
	err = s.deps.Sink.Emit(request, e)
	return errors.Join(err, request.Err())
}
