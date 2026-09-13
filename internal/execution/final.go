package execution

import (
	"context"
	"errors"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

// WaitFinal observes the immutable final record under the caller's wait
// deadline. It validates metadata and the independent publisher's attestation
// that uploads completed; it does not download workload output. Full stream
// length/checksum verification belongs to explicit logs retrieval.
func WaitFinal(ctx context.Context, store execprotocol.Store, sub Submission, pollInterval time.Duration) Result {
	r := Result{
		SchemaVersion: 1, Command: "exec", CommandID: sub.Binding.CommandID,
		SSMCommandID: sub.SSMCommandID, InstanceID: sub.Binding.InstanceID,
	}
	finish := func(outcome string, exit int, message string) Result {
		r.Outcome, r.Code, r.ExitCode, r.Message = outcome, outcome, exit, message
		r.OK = outcome == "remote_exit" && exit == 0
		return r
	}
	stopped := func(err error) Result {
		if errors.Is(err, context.Canceled) {
			return finish("interrupted", 4, "observation stopped; the remote command may continue; recover using the command ID")
		}
		return finish("observation_timeout", 4, "observation deadline expired; the remote command may continue; recover using the command ID")
	}
	if sub.Binding.Validate() != nil || !execprotocol.ValidSSMCommandID(sub.SSMCommandID) {
		return finish("result_corrupt", 1, "cannot verify the submitted command identity")
	}
	if store == nil {
		return finish("api_failed", 1, "cannot read durable command results; check credentials, permissions and connectivity")
	}
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	const maxPollInterval = 5 * time.Second
	if pollInterval > maxPollInterval {
		pollInterval = maxPollInterval
	}
	key := execprotocol.ObjectKey(sub.Binding.Scope, sub.Binding.CommandID, "result.json")
	for {
		if err := ctx.Err(); err != nil {
			return stopped(err)
		}
		record, _, err := execprotocol.ReadRecord(ctx, store, key)
		if err == nil {
			if record.Kind != "result" || record.Binding != sub.Binding || record.SSMCommandID != sub.SSMCommandID {
				return finish("result_corrupt", 1, "durable result does not match the submitted command identity")
			}
			// Only identity-bound, strictly validated metadata may be returned as
			// workload evidence, including when retention or publication failed.
			r.Workload, r.Capture, r.Publication, r.Streams = record.Workload, record.Capture, record.Publication, record.Streams
			expires, _ := execprotocol.ParseTimestamp(record.ExpiresAt)
			if !time.Now().Before(expires) {
				return finish("expired", 1, "the command result retention deadline has passed")
			}
			if record.Workload.Status == "execution_timeout" {
				return finish("execution_timeout", 4, "the remote workload exceeded its execution deadline")
			}
			if record.Capture != "complete" || record.Publication != "complete" || record.Streams.Stdout.Upload != "complete" || record.Streams.Stderr.Upload != "complete" {
				return finish("result_incomplete", 1, "the remote result was finalized with incomplete capture or publication")
			}
			switch record.Workload.Status {
			case "exited":
				return finish("remote_exit", *record.Workload.ExitCode, "remote workload exit and completed publication recorded")
			case "signaled":
				return finish("remote_signal", 128+*record.Workload.Signal, "remote workload signal and completed publication recorded")
			case "runner_setup_failed":
				return finish("setup_failed", 1, "the remote runner could not prepare the workload")
			default:
				return finish("result_incomplete", 1, "the remote runner could not establish a complete workload result")
			}
		}
		if ctx.Err() != nil {
			return stopped(ctx.Err())
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return stopped(err)
		}
		if errors.Is(err, execprotocol.ErrUnsupportedSchema) {
			return finish("unsupported_schema", 1, "the command result uses an unsupported schema version")
		}
		if errors.Is(err, execprotocol.ErrCorrupt) {
			return finish("result_corrupt", 1, "durable result metadata is invalid or does not match its object identity")
		}
		if !errors.Is(err, execprotocol.ErrNotFound) {
			return finish("api_failed", 1, "cannot read durable command results; check credentials, permissions and connectivity")
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return stopped(ctx.Err())
		case <-timer.C:
		}
		if pollInterval >= maxPollInterval/2 {
			pollInterval = maxPollInterval
		} else {
			pollInterval *= 2
		}
	}
}
