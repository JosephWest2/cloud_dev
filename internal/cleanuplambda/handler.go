// Package cleanuplambda binds the shared cleanup service to trusted Lambda
// configuration. Scheduled input contains correlation only.
package cleanuplambda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
}

type Record struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	RequestID     string                `json:"request_id"`
	Scope         expiry.Scope          `json:"scope"`
	Correlation   expiry.ScheduledInput `json:"correlation"`
	EmittedAt     string                `json:"emitted_at"`
	Code          string                `json:"code,omitempty"`
	OK            bool                  `json:"ok"`
	Complete      bool                  `json:"complete"`
	Event         *expiry.Event         `json:"event,omitempty"`
}

func (h Handler) Handle(ctx context.Context, raw json.RawMessage) (expiry.Result, error) {
	var result expiry.Result
	s, err := Environment(h.Getenv)
	if err != nil {
		return result, err
	}
	input, err := Decode(raw)
	if err != nil {
		return result, err
	}
	lc, ok := lambdacontext.FromContext(ctx)
	if !ok || !regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`).MatchString(lc.AwsRequestID) {
		return result, errors.New("cleanup_invocation_invalid")
	}
	// The outer context retains time for the terminal event after service timeout.
	run, stop := context.WithTimeout(ctx, InvocationTimeout)
	defer stop()
	setup, cancel := context.WithTimeout(run, 15*time.Second)
	runner, journal, err := h.Factory(setup, s, lc.AwsRequestID, input)
	cancel()
	if err != nil {
		return result, errors.New("cleanup_setup_failed")
	}
	record := Record{SchemaVersion: 1, Kind: "invocation_start", RequestID: lc.AwsRequestID, Scope: s.Scope, Correlation: input}
	record.EmittedAt, err = expiry.Timestamp(h.Clock.Now())
	if err != nil {
		return result, errors.New("cleanup_clock_invalid")
	}
	if err = journal.Record(run, record); err != nil {
		return result, errors.New("cleanup_evidence_unavailable")
	}
	result, err = runner.Run(run, false)
	stop()
	record.Kind = "invocation_end"
	record.Code = result.Code
	record.OK = result.OK && err == nil
	record.Complete = result.Complete
	record.EmittedAt, _ = expiry.Timestamp(h.Clock.Now())
	if journal.Record(ctx, record) != nil {
		return result, errors.New("cleanup_evidence_unavailable")
	}
	if err != nil || !result.OK || !result.Complete {
		return result, errors.New("cleanup_incomplete")
	}
	return result, nil
}
