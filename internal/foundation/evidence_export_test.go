package foundation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

func verifyEvidenceExport(t *testing.T, m config.Manifest, c config.Cleanup, e config.CleanupEvidence, resources map[string]map[string]json.RawMessage) {
	t.Helper()
	var target []struct {
		Input string `json:"input_template"`
	}
	if json.Unmarshal(resources["aws_pipes_pipe.cleanup_failures"]["target_parameters"], &target) != nil || len(target) != 1 {
		t.Fatal("missing Pipe target input")
	}
	actual := target[0].Input
	if actual != config.PipeInputTemplate || strings.Contains(actual, `\u003c`) || strings.Contains(actual, "receiptHandle") {
		t.Fatalf("encoded or unsafe Pipe template: %s", actual)
	}
	// Exercise the actual rendered Terraform string using AWS's documented typed
	// substitutions. These controlled envelopes test field preservation, not live
	// Pipes behavior or Scheduler's classification/retry of an AWS delivery failure.
	attribute := func(value string) any {
		return map[string]any{"stringValue": value, "dataType": "String"}
	}
	schedulerBody := func(attempt string) (map[string]any, map[string]any) {
		correlation := map[string]any{
			"schema_version": float64(1), "execution_id": "schedule-123",
			"schedule_arn": c.Schedule.ARN, "scheduled_time": "2026-09-14T12:00:00Z", "attempt_number": attempt,
		}
		payload, err := json.Marshal(correlation)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"FunctionName": c.Function.ARN, "InvocationType": "Event", "Payload": string(payload)}, correlation
	}
	deniedBody, _ := schedulerBody("1")
	// Model a third-attempt payload after two retries as a controlled example.
	// AWS documents attempt-number as the current invocation's counter, but does
	// not specify here which attempt's payload a DLQ record retains. This tests
	// preservation of the supplied example, not that undocumented selection.
	exhaustedBody, exhaustedCorrelation := schedulerBody("3")
	schedulerAttributes := func(code, retries string) map[string]any {
		return map[string]any{
			"ERROR_CODE": attribute(code), "ERROR_MESSAGE": attribute("controlled delivery failure"),
			"EXECUTION_ID": attribute("schedule-123"), "SCHEDULE_ARN": attribute(c.Schedule.ARN),
			"SCHEDULED_TIME": attribute("2026-09-14T12:00:00Z"), "TARGET_ARN": attribute(c.Function.ARN),
			"RETRY_ATTEMPTS": attribute(retries), "IS_PAYLOAD_TRUNCATED": attribute("false"),
			"CONTAINS_INVALID_PAYLOAD": attribute("false"),
		}
	}
	exhaustedAttributes := schedulerAttributes("TooManyRequestsException", "2")
	exhaustedAttributes["EXHAUSTED_RETRY_CONDITION"] = attribute("MaximumRetryAttempts")
	for _, fixture := range []struct {
		name      string
		body      any
		attrs     map[string]any
		exhausted bool
	}{
		{"scheduler_permanent_denial", deniedBody, schedulerAttributes("AccessDeniedException", "0"), false},
		{"scheduler_retry_exhausted_controlled", exhaustedBody, exhaustedAttributes, true},
		{"lambda_async_failure", map[string]any{"requestContext": map[string]any{"requestId": "lambda-123", "condition": "RetriesExhausted"}, "requestPayload": map[string]any{"execution_id": "schedule-456"}, "responsePayload": map[string]any{"errorType": "Runtime.InvalidEntrypoint"}}, map[string]any{}, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			values := map[string]any{"<$.messageId>": "sqs-123", "<$.body>": fixture.body, "<$.messageAttributes>": fixture.attrs, "<$.attributes.SentTimestamp>": "1789387200000", "<aws.pipes.event.ingestion-time>": "2026-09-14T12:30:00Z", "<aws.pipes.pipe-arn>": e.Pipe.ARN, "<aws.pipes.source-arn>": e.Queue.ARN}
			rendered := actual
			for key, value := range values {
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(rendered, key) != 1 {
					t.Fatal("missing/repeated literal placeholder", key)
				}
				rendered = strings.ReplaceAll(rendered, key, string(encoded))
			}
			var got map[string]any
			if json.Unmarshal([]byte(rendered), &got) != nil {
				t.Fatal("transformation is not JSON", rendered)
			}
			if !reflect.DeepEqual(got["body"], fixture.body) || !reflect.DeepEqual(got["messageAttributes"], fixture.attrs) || got["messageId"] != "sqs-123" || got["SentTimestamp"] != "1789387200000" || got["ingested_at"] != "2026-09-14T12:30:00Z" || got["pipe_arn"] != e.Pipe.ARN || got["source_arn"] != e.Queue.ARN || len(got) != 9 {
				t.Fatal("failure envelope lost identity or diagnostics", got)
			}
			attrs := got["messageAttributes"].(map[string]any)
			if fixture.exhausted {
				if !reflect.DeepEqual(attrs["RETRY_ATTEMPTS"], attribute("2")) || !reflect.DeepEqual(attrs["EXHAUSTED_RETRY_CONDITION"], attribute("MaximumRetryAttempts")) {
					t.Fatal("controlled Scheduler retry/exhaustion fields lost", attrs)
				}
				var resolved map[string]any
				if json.Unmarshal([]byte(got["body"].(map[string]any)["Payload"].(string)), &resolved) != nil || !reflect.DeepEqual(resolved, exhaustedCorrelation) {
					t.Fatal("original Scheduler input correlation lost", got["body"])
				}
			} else if _, present := attrs["EXHAUSTED_RETRY_CONDITION"]; present {
				t.Fatal("non-exhausted envelope gained Scheduler exhaustion", attrs)
			}
		})
	}

	for key, want := range e.Alarms {
		b, _ := json.Marshal(resources[`aws_cloudwatch_metric_alarm.cleanup["`+key+`"]`])
		var actual struct {
			AlarmName string `json:"alarm_name"`
			config.EvidenceAlarm
			Datapoints int `json:"datapoints_to_alarm"`
		}
		if json.Unmarshal(b, &actual) != nil {
			t.Fatal("alarm render invalid", key)
		}
		actual.Name = actual.AlarmName
		actual.ARN = want.ARN // mocked ARN is synthetic
		if !reflect.DeepEqual(actual.EvidenceAlarm, want) || actual.Datapoints != want.EvaluationPeriods {
			t.Fatalf("alarm export drift %s: %+v want %+v", key, actual, want)
		}
	}
}
