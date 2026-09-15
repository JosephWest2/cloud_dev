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
	// substitutions. This is a controlled contract test, not a live Pipes emulator.
	for _, body := range []any{
		map[string]any{"schema_version": float64(1), "execution_id": "schedule-123", "scheduled_time": "2026-09-14T12:00:00Z"},
		map[string]any{"requestContext": map[string]any{"requestId": "lambda-123", "condition": "RetriesExhausted"}, "requestPayload": map[string]any{"execution_id": "schedule-456"}, "responsePayload": map[string]any{"errorType": "Runtime.InvalidEntrypoint"}},
	} {
		attrs := map[string]any{"ERROR_CODE": map[string]any{"stringValue": "AccessDeniedException", "dataType": "String"}, "EXECUTION_ID": map[string]any{"stringValue": "schedule-123", "dataType": "String"}}
		values := map[string]any{"<$.messageId>": "sqs-123", "<$.body>": body, "<$.messageAttributes>": attrs, "<$.attributes.SentTimestamp>": "1789387200000", "<aws.pipes.event.ingestion-time>": "2026-09-14T12:30:00Z", "<aws.pipes.pipe-arn>": e.Pipe.ARN, "<aws.pipes.source-arn>": e.Queue.ARN}
		rendered := actual
		for key, value := range values {
			encoded, _ := json.Marshal(value)
			if strings.Count(rendered, key) != 1 {
				t.Fatal("missing/repeated literal placeholder", key)
			}
			rendered = strings.ReplaceAll(rendered, key, string(encoded))
		}
		var got map[string]any
		if json.Unmarshal([]byte(rendered), &got) != nil {
			t.Fatal("transformation is not JSON", rendered)
		}
		if !reflect.DeepEqual(got["body"], body) || !reflect.DeepEqual(got["messageAttributes"], attrs) || got["messageId"] != "sqs-123" || got["SentTimestamp"] != "1789387200000" || got["ingested_at"] != "2026-09-14T12:30:00Z" || got["pipe_arn"] != e.Pipe.ARN || got["source_arn"] != e.Queue.ARN || len(got) != 9 {
			t.Fatal("failure envelope lost identity or diagnostics", got)
		}
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
