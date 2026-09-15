// Package cleanuplambda binds the shared cleanup service to trusted Lambda
// configuration. Scheduled input contains correlation only.
package cleanuplambda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"regexp"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-lambda-go/lambdacontext"
)

const InvocationTimeout = 165 * time.Second

type Settings struct {
	Scope    expiry.Scope
	LogGroup string
}

func Environment(get func(string) string) (Settings, error) {
	if get == nil {
		return Settings{}, errors.New("cleanup_configuration_invalid")
	}
	s := Settings{Scope: expiry.Scope{Account: get("DEVBOX_ACCOUNT"), Region: get("DEVBOX_REGION"), Deployment: get("DEVBOX_DEPLOYMENT"), Owner: get("DEVBOX_OWNER")}, LogGroup: get("DEVBOX_LOG_GROUP")}
	if s.Scope.Validate() != nil || s.Scope.Region != get("AWS_REGION") || s.LogGroup != "/aws/lambda/devbox-"+s.Scope.Deployment+"-"+s.Scope.Owner+"-cleanup" {
		return Settings{}, errors.New("cleanup_configuration_invalid")
	}
	return s, nil
}

// Decode rejects duplicate keys, alternate casing, nulls and trailing data as
// well as unknown authority fields; encoding/json struct matching is too broad.
func Decode(raw json.RawMessage) (expiry.ScheduledInput, error) {
	var input expiry.ScheduledInput
	bad := errors.New("cleanup_input_invalid")
	if len(raw) > 4096 {
		return input, bad
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return input, bad
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return input, bad
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return input, bad
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return input, bad
		}
		if key == "schema_version" {
			if json.Unmarshal(value, &input.SchemaVersion) != nil {
				return input, bad
			}
			continue
		}
		var s string
		if json.Unmarshal(value, &s) != nil || len(s) > 256 {
			return input, bad
		}
		switch key {
		case "schedule_arn":
			if !regexp.MustCompile(`^arn:aws:scheduler:[a-z0-9-]+:[0-9]{12}:schedule/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(s) {
				return input, bad
			}
			input.ScheduleARN = s
		case "scheduled_time":
			if _, err := expiry.ParseTimestamp(s); err != nil {
				return input, bad
			}
			input.ScheduledTime = s
		case "execution_id":
			if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(s) {
				return input, bad
			}
			input.ExecutionID = s
		case "attempt_number":
			if !regexp.MustCompile(`^[0-9]{1,4}$`).MatchString(s) {
				return input, bad
			}
			input.AttemptNumber = s
		default:
			return input, bad
		}
	}
	if _, err = d.Token(); err != nil || input.SchemaVersion != 1 {
		return input, bad
	}
	if _, err = d.Token(); err != io.EOF {
		return input, bad
	}
	return input, nil
}

type Runner interface {
	Run(context.Context, bool) (expiry.Result, error)
}
type Journal interface {
	expiry.Sink
	Record(context.Context, Record) error
}
type Factory func(context.Context, Settings, string, expiry.ScheduledInput) (Runner, Journal, error)
type Handler struct {
	Getenv  func(string) string
	Factory Factory
	Clock   expiry.Clock
	// Bootstrap records sanitized failures when the AWS journal cannot initialize.
	Bootstrap func(Record)
}

type Record struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	RunID         string                `json:"run_id"`
	RequestID     string                `json:"request_id"`
	Scope         expiry.Scope          `json:"scope"`
	Correlation   expiry.ScheduledInput `json:"correlation"`
	EmittedAt     string                `json:"emitted_at"`
	Code          string                `json:"code,omitempty"`
	OK            bool                  `json:"ok"`
	Complete      bool                  `json:"complete"`
	Partial       bool                  `json:"partial"`
	Deadline      bool                  `json:"deadline"`
	Result        *expiry.Result        `json:"result,omitempty"`
	Event         *expiry.Event         `json:"event,omitempty"`
}

// Handle returns only stable codes to the Lambda runtime and its asynchronous
// failure destination. Raw payloads, configuration and SDK errors are never logged.
func (h Handler) Handle(ctx context.Context, raw json.RawMessage) (result expiry.Result, retErr error) {
	record := Record{SchemaVersion: 1, Kind: "invocation_start"}
	if lc, ok := lambdacontext.FromContext(ctx); ok && regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`).MatchString(lc.AwsRequestID) {
		record.RequestID, record.RunID = lc.AwsRequestID, lc.AwsRequestID
	}
	if h.Clock == nil {
		h.Clock = expiry.SystemClock{}
	}
	record.EmittedAt, _ = expiry.Timestamp(h.Clock.Now())
	var journal Journal
	started := false
	defer func() {
		emit := func(r Record) error {
			if journal != nil {
				bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				return journal.Record(bounded, r)
			}
			if h.Bootstrap != nil {
				h.Bootstrap(r)
			} else {
				data, _ := json.Marshal(r)
				log.New(os.Stderr, "", 0).Print(string(data))
			}
			return nil
		}
		if !started {
			_ = emit(record)
		}
		record.Kind = "invocation_end"
		record.EmittedAt, _ = expiry.Timestamp(h.Clock.Now())
		record.Code = result.Code
		record.OK = retErr == nil && result.OK && result.Complete && result.ScanComplete && result.ExitCode == 0
		record.Complete = record.OK
		record.Partial = result.ExitCode == 3 || !result.Complete
		record.Deadline = result.ExitCode == 4 || ctx.Err() != nil
		if result.SchemaVersion != 0 {
			record.Result = &result
		}
		if retErr != nil && record.Code == "" {
			record.Code = retErr.Error()
		}
		if result.SchemaVersion == 0 {
			summary := record
			summary.Kind = "summary"
			_ = emit(summary)
		}
		if emit(record) != nil {
			retErr = errors.New("cleanup_evidence_unavailable")
		}
	}()
	s, err := Environment(h.Getenv)
	if err != nil {
		return result, err
	}
	record.Scope = s.Scope
	input, err := Decode(raw)
	if err != nil {
		return result, err
	}
	record.Correlation = input
	if record.RequestID == "" {
		return result, errors.New("cleanup_invocation_invalid")
	}
	if record.EmittedAt == "" {
		return result, errors.New("cleanup_clock_invalid")
	}
	run, stop := context.WithTimeout(ctx, InvocationTimeout)
	defer stop()
	setup, cancel := context.WithTimeout(run, 15*time.Second)
	if h.Factory == nil {
		cancel()
		return result, errors.New("cleanup_setup_failed")
	}
	runner, j, err := h.Factory(setup, s, record.RequestID, input)
	cancel()
	journal = j
	if err != nil || runner == nil || journal == nil {
		return result, errors.New("cleanup_setup_failed")
	}
	if err = journal.Record(run, record); err != nil {
		return result, errors.New("cleanup_evidence_unavailable")
	}
	started = true
	result, err = runner.Run(run, false)
	if err != nil || !result.OK || !result.Complete || !result.ScanComplete || result.ExitCode != 0 {
		return result, errors.New("cleanup_incomplete")
	}
	return result, nil
}
