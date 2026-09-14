package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

// WaitBatchReady shares one five-minute budget, including document validation
// and queue time. Each worker keeps its allocation identity and has an isolated
// readiness outcome; a failed worker never cancels another worker's observation.
// No launch or replacement operation is reachable from this method.
func (s *Service) WaitBatchReady(ctx context.Context, manifest config.Manifest, workers []WorkerOutcome, progress io.Writer) error {
	return s.waitBatchReady(ctx, manifest, workers, progress, nil)
}

// prepare is supplied only by public launch orchestration with validated launch
// evidence. It must verify pending workers fully before they reach an SSM probe.
func (s *Service) waitBatchReady(ctx context.Context, manifest config.Manifest, workers []WorkerOutcome, progress io.Writer, prepare func(context.Context, *WorkerOutcome) error) error {
	if len(workers) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	errs := make([]error, len(workers))
	counts := map[string]int{}
	for _, worker := range workers {
		counts[worker.ID]++
	}
	queue := make(chan int, len(workers))
	pending := []int{}
	for n := range workers {
		w := &workers[n]
		switch {
		case !instanceRE.MatchString(w.ID):
			errs[n] = failure("target_invalid", "batch readiness requires an exact EC2 instance ID")
			observationError(&w.Instance, errs[n])
		case counts[w.ID] != 1:
			errs[n] = failure("target_duplicate", "batch readiness received a repeated instance identity; inspect the request before probing")
			observationError(&w.Instance, errs[n])
		case w.Status != "allocated" && w.Status != "historical" && !(w.Status == "not_observed" && prepare != nil):
			errs[n] = failure("worker_not_verified", "worker allocation identity has not been verified; resume request inspection before readiness")
			originalCode := w.ObservationCode
			observationError(&w.Instance, errs[n])
			if originalCode != "" {
				w.ObservationCode = originalCode
			}
		case w.Status == "historical" || batchCannotRun(w.State):
			errs[n] = batchNotRunning(&w.Instance)
		case w.Status == "not_observed":
			pending = append(pending, n)
		default:
			queue <- n
		}
	}
	// A set of slow startup observations must not occupy every slot before
	// workers whose launch identities are already verified can make progress.
	for _, n := range pending {
		queue <- n
	}
	close(queue)
	if len(queue) > 0 {
		var verifyErr error
		if s == nil || s.API == nil {
			verifyErr = failure("probe_unavailable", "instance observation is unavailable; restore access and retry readiness")
		} else if verifyErr = ctx.Err(); verifyErr == nil {
			verifyErr = s.VerifyProbe(ctx, manifest)
		}
		if verifyErr != nil {
			for n := range queue {
				errs[n] = verifyErr
				observationError(&workers[n].Instance, verifyErr)
			}
		} else {
			output := newBatchReadyProgress(ctx, progress)
			var wg sync.WaitGroup
			concurrency := min(4, len(queue))
			for worker := 0; worker < concurrency; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for n := range queue {
						if workers[n].Status == "not_observed" && prepare != nil {
							output.write(workers[n].ID, "allocation=not_observed; waiting for exact launch and root-volume verification")
							if errs[n] = prepare(ctx, &workers[n]); errs[n] != nil {
								observationError(&workers[n].Instance, errs[n])
								continue
							}
						}
						errs[n] = s.waitBatchWorker(ctx, manifest, &workers[n], output)
					}
				}()
			}
			wg.Wait()
			outputErr := output.finish()
			// Output failure belongs to the operation, while successful worker
			// readiness remains useful evidence. Suppress subsequent writes but
			// let every peer finish before reporting the failure.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if outputErr != nil {
				return outputErr
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func batchCannotRun(state string) bool {
	return state == "terminated" || state == "shutting-down" || state == "stopped" || state == "stopping"
}

func batchNotRunning(instance *Instance) error {
	err := failure("instance_not_running", "instance cannot become ready in its current EC2 state; inspect or tear down by instance ID")
	instance.Readiness, instance.ObservationCode, instance.HostKey = "not_ready", "instance_not_running", ""
	return err
}

// Allocation pins are never replaced by a later observation. A mismatched
// request/attempt binding stops this worker's probe and leaves its original
// identity available for inspection and teardown.
func batchReadyIdentity(expected, observed Instance) bool {
	if expected.ID != observed.ID {
		return false
	}
	for _, pair := range [][2]string{
		{expected.Name, observed.Name},
		{expected.RequestID, observed.RequestID}, {expected.Profile, observed.Profile},
		{expected.CreatedAt, observed.CreatedAt}, {expected.Image, observed.Image},
		{expected.Type, observed.Type}, {expected.Market, observed.Market},
		{expected.TemplateID, observed.TemplateID}, {expected.TemplateVersion, observed.TemplateVersion},
		{expected.Group, observed.Group}, {expected.BaseName, observed.BaseName},
		{expected.AttemptID, observed.AttemptID}, {expected.SubnetID, observed.SubnetID},
		{expected.AvailabilityZone, observed.AvailabilityZone},
	} {
		if (expected.AttemptID != "" || pair[0] != "") && pair[0] != pair[1] {
			return false
		}
	}
	return true
}

func (s *Service) waitBatchWorker(ctx context.Context, manifest config.Manifest, worker *WorkerOutcome, progress *batchReadyProgress) error {
	last := ""
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			observationError(&worker.Instance, err)
			return err
		}
		fresh, err := s.resolveBatchReady(ctx, worker.ID)
		if err != nil {
			observationError(&worker.Instance, err)
			return err
		}
		if len(fresh) != 1 || !batchReadyIdentity(worker.Instance, fresh[0]) {
			err = failure("worker_identity_changed", "worker launch identity differs from the request; inspect the instance without sending a readiness probe")
			observationError(&worker.Instance, err)
			return err
		}
		worker.State = fresh[0].State
		if batchCannotRun(worker.State) {
			err = batchNotRunning(&worker.Instance)
		} else {
			expected := worker.Instance
			err = s.observeWithGuard(ctx, manifest, &worker.Instance, func(fresh Instance) error {
				if !batchReadyIdentity(expected, fresh) {
					return failure("worker_identity_changed", "worker launch identity differs from the request; inspect the instance without sending a readiness probe")
				}
				return nil
			})
		}
		state := fmt.Sprintf("ec2=%s ssm=%s bootstrap=%s readiness=%s", worker.State, worker.SSM, worker.Bootstrap, worker.Readiness)
		if state != last {
			progress.write(worker.ID, state)
			last = state
		}
		if err != nil {
			return err
		}
		switch worker.Readiness {
		case "ready":
			return nil
		case "failed":
			worker.ObservationCode = "bootstrap_failed"
			return failure("bootstrap_failed", "bootstrap reported failure; retain the probe command ID for inspection; remove the instance with devbox down INSTANCE_ID after investigation")
		case "not_ready":
			return batchNotRunning(&worker.Instance)
		}
		if err = s.pause(ctx, attempt); err != nil {
			observationError(&worker.Instance, err)
			return err
		}
	}
}

func (s *Service) resolveBatchReady(ctx context.Context, id string) ([]Instance, error) {
	fresh, err := s.Resolve(ctx, id)
	var missing *Failure
	if errors.As(err, &missing) && missing.Code == "target_unresolved" {
		// Resolve intentionally selects live targets. An exact scoped reread
		// can retain a worker that terminated after allocation reconciliation,
		// without interpreting an empty inventory result as termination.
		observed, lookupErr := s.inventory(ctx, id, "", "")
		if lookupErr != nil {
			return observed, lookupErr
		}
		if len(observed) == 1 && observed[0].ID == id {
			return observed, nil
		}
	}
	return fresh, err
}

type batchReadyProgress struct {
	ctx    context.Context
	events chan string
	done   chan struct{}
	err    error
}

func newBatchReadyProgress(ctx context.Context, writer io.Writer) *batchReadyProgress {
	p := &batchReadyProgress{ctx: ctx, events: make(chan string, 4), done: make(chan struct{})}
	if writer == nil {
		close(p.done)
		return p
	}
	// A single writer serializes output without holding readiness workers
	// behind a blocked pipe. io.Writer cannot be forcibly interrupted; on a
	// deadline at most this one write can remain pending while the batch ends.
	go func() {
		defer close(p.done)
		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-p.events:
				if !ok || ctx.Err() != nil {
					return
				}
				n, err := io.WriteString(writer, line)
				if err != nil || n != len(line) {
					// Writer errors may expose local paths or transport details.
					p.err = failure("output_unavailable", "cannot write batch readiness progress; inspect the retained instance identities")
					return
				}
			}
		}
	}()
	return p
}

func (p *batchReadyProgress) write(id, state string) {
	select {
	case <-p.ctx.Done():
	case <-p.done:
	case p.events <- fmt.Sprintf("devbox: %s %s\n", id, state):
	default:
		// Progress is advisory: a slow consumer may miss an intermediate line,
		// but must never hold up this worker or workers still in the queue.
	}
}

func (p *batchReadyProgress) finish() error {
	close(p.events)
	select {
	case <-p.done:
		return p.err
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}
