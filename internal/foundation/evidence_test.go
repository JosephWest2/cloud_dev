package foundation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/cleanuplambda"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/pipes"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func (f *fake) DescribePipe(_ context.Context, in *pipes.DescribePipeInput, _ ...func(*pipes.Options)) (*pipes.DescribePipeOutput, error) {
	out := &pipes.DescribePipeOutput{}
	return out, f.respond("DescribePipe", in, out)
}
func (f *fake) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	out := &sqs.GetQueueAttributesOutput{}
	return out, f.respond("GetQueueAttributes", in, out)
}
func (f *fake) DescribeAlarms(_ context.Context, in *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error) {
	out := &cloudwatch.DescribeAlarmsOutput{}
	return out, f.respond("DescribeAlarms", in, out)
}
func (f *fake) DescribeMetricFilters(_ context.Context, in *cloudwatchlogs.DescribeMetricFiltersInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeMetricFiltersOutput, error) {
	out := &cloudwatchlogs.DescribeMetricFiltersOutput{}
	return out, f.respond("DescribeMetricFilters", in, out)
}
func (f *fake) DescribeLogStreams(_ context.Context, in *cloudwatchlogs.DescribeLogStreamsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
	out := &cloudwatchlogs.DescribeLogStreamsOutput{}
	return out, f.respond("DescribeLogStreams", in, out)
}
func (f *fake) FilterLogEvents(_ context.Context, in *cloudwatchlogs.FilterLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error) {
	out := &cloudwatchlogs.FilterLogEventsOutput{}
	return out, f.respond("FilterLogEvents", in, out)
}

func evidenceFixture(t *testing.T) (*fake, config.Manifest, config.Cleanup, config.CleanupEvidence, CleanupClients) {
	t.Helper()
	f, m, c := cleanupFixture(t)
	name := "devbox-" + m.Deployment + "-" + m.Owner
	regional := m.Region + ":" + m.Account
	e := config.CleanupEvidence{SchemaVersion: 1, Queue: config.EvidenceQueue{Name: name + "-evidence", ARN: "arn:aws:sqs:" + regional + ":" + name + "-evidence", URL: "https://sqs." + m.Region + ".amazonaws.com/" + m.Account + "/" + name + "-evidence", RetentionSeconds: 1209600, VisibilitySeconds: 1800, SSESQS: true}, Pipe: config.EvidencePipe{Name: name + "-evidence", ARN: "arn:aws:pipes:" + regional + ":pipe/" + name + "-evidence", DesiredState: "RUNNING", BatchSize: 1, InputTemplate: config.PipeInputTemplate}, Logs: config.CleanupLogs{Name: "/devbox/" + name + "/failures", ARN: "arn:aws:logs:" + regional + ":log-group:/devbox/" + name + "/failures", RetentionDays: 30}, Stream: "failures", SuccessFilter: config.EvidenceFilter{Name: "successful-completion", Pattern: config.SuccessPattern(m), Namespace: "Devbox/Cleanup/" + c.Schedule.Name, MetricName: "SuccessfulCompletion"}, Alarms: config.ExpectedEvidenceAlarms(m, c), DeliveryMetric: config.EvidenceMetric{Namespace: "AWS/Scheduler", MetricName: "InvocationAttemptCount", Dimensions: map[string]string{"ScheduleGroup": c.Schedule.GroupName}}}
	for suffix, r := range map[string]*config.Role{"evidence": &e.PipeRole, "health": &e.HealthRole} {
		*r = c.ExecutionRole
		r.ARN = "arn:aws:iam::" + m.Account + ":role/" + name + "-" + suffix
		r.PolicyName = "devbox-" + suffix
	}
	c.Evidence, _ = json.Marshal(e)
	m.Cleanup, _ = json.Marshal(c)
	write := func(key string, value any) { b, _ := json.Marshal(value); f.responses[key] = string(b) }
	write("DescribePipe", map[string]any{"Arn": e.Pipe.ARN, "Name": e.Pipe.Name, "CurrentState": "RUNNING", "DesiredState": "RUNNING", "RoleArn": e.PipeRole.ARN, "Source": e.Queue.ARN, "Target": e.Logs.ARN, "SourceParameters": map[string]any{"SqsQueueParameters": map[string]any{"BatchSize": 1}}, "TargetParameters": map[string]any{"InputTemplate": e.Pipe.InputTemplate, "CloudWatchLogsParameters": map[string]any{"LogStreamName": "failures"}}})
	write("GetQueueAttributes", map[string]any{"Attributes": map[string]string{"QueueArn": e.Queue.ARN, "MessageRetentionPeriod": "1209600", "VisibilityTimeout": "1800", "SqsManagedSseEnabled": "true", "ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "0", "ApproximateNumberOfMessagesDelayed": "0"}})
	write("DescribeLogStreams", map[string]any{"LogStreams": []any{map[string]any{"LogStreamName": "failures", "Arn": e.Logs.ARN + ":log-stream:failures"}}})
	alarms := []any{}
	for _, a := range e.Alarms {
		dimensions := []any{}
		for key, value := range a.Dimensions {
			dimensions = append(dimensions, map[string]string{"Name": key, "Value": value})
		}
		alarms = append(alarms, map[string]any{"AlarmName": a.Name, "AlarmArn": a.ARN, "StateValue": "OK", "Namespace": a.Namespace, "MetricName": a.MetricName, "Dimensions": dimensions, "Statistic": a.Statistic, "Threshold": a.Threshold, "ComparisonOperator": a.ComparisonOperator, "Period": a.Period, "EvaluationPeriods": a.EvaluationPeriods, "DatapointsToAlarm": a.EvaluationPeriods, "TreatMissingData": a.TreatMissingData})
	}
	write("DescribeAlarms", map[string]any{"MetricAlarms": alarms})
	write("DescribeMetricFilters", map[string]any{"MetricFilters": []any{map[string]any{"FilterName": e.SuccessFilter.Name, "LogGroupName": c.Logs.Name, "FilterPattern": e.SuccessFilter.Pattern, "MetricTransformations": []any{map[string]any{"MetricName": e.SuccessFilter.MetricName, "MetricNamespace": e.SuccessFilter.Namespace, "MetricValue": "1", "Unit": "Count"}}}}})
	record := successfulRecord(m, c)
	message, _ := json.Marshal(record)
	write("FilterLogEvents", map[string]any{"Events": []any{map[string]string{"Message": string(message)}}})
	clients := CleanupClients{Lambda: f, Scheduler: f, Logs: f, IAM: f, Pipe: f, Queue: f, CloudWatch: f, EvidenceLogs: f, Now: func() time.Time { return time.Date(2026, 9, 14, 12, 5, 0, 0, time.UTC) }}
	return f, m, c, e, clients
}
func successfulRecord(m config.Manifest, c config.Cleanup) cleanuplambda.Record {
	scope := expiry.Scope{Account: m.Account, Region: m.Region, Deployment: m.Deployment, Owner: m.Owner}
	return cleanuplambda.Record{SchemaVersion: 1, Kind: "invocation_end", RunID: "request-123", RequestID: "request-123", Scope: scope, Correlation: expiry.ScheduledInput{SchemaVersion: 1, ScheduleARN: c.Schedule.ARN, ScheduledTime: "2026-09-14T12:00:00Z", ExecutionID: "execution-123", AttemptNumber: "1"}, EmittedAt: "2026-09-14T12:01:00Z", Code: "cleanup_no_candidates", OK: true, Complete: true, Result: &expiry.Result{SchemaVersion: 1, Command: "cleanup", OK: true, Code: "cleanup_no_candidates", Scope: scope, EvaluatedAt: "2026-09-14T12:00:01Z", CompletedAt: "2026-09-14T12:00:59Z", ScanComplete: true, Complete: true, Instances: []expiry.Outcome{}, Errors: []expiry.Problem{}}}
}
func TestEvidenceHealthySettingsAndNarrowReads(t *testing.T) {
	f, m, c, e, clients := evidenceFixture(t)
	if _, err := config.DecodeEvidence(c.Evidence, m, c); err != nil {
		t.Fatal(err)
	}
	if err := checkEvidenceRoute(context.Background(), clients, e); err != nil {
		t.Fatal(err)
	}
	if err := checkEvidenceAlarms(context.Background(), clients, e, c); err != nil {
		t.Fatal(err)
	}
	if err := checkRecentCompletion(context.Background(), f, m, c, clients.Now()); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.inputs["DescribePipe"].(*pipes.DescribePipeInput).Name) != e.Pipe.Name || aws.ToString(f.inputs["GetQueueAttributes"].(*sqs.GetQueueAttributesInput).QueueUrl) != e.Queue.URL || aws.ToString(f.inputs["FilterLogEvents"].(*cloudwatchlogs.FilterLogEventsInput).LogGroupName) != c.Logs.Name || len(f.inputs["DescribeAlarms"].(*cloudwatch.DescribeAlarmsInput).AlarmNames) != 16 {
		t.Fatal("health request was not exact")
	}
}
func TestEvidenceRouteAndAlarmDrift(t *testing.T) {
	for _, tc := range []struct{ method, from, to string }{
		{"DescribePipe", "RUNNING", "STOPPED"}, {"DescribePipe", `"BatchSize":1`, `"BatchSize":10`}, {"DescribePipe", `"SourceParameters":{`, `"SourceParameters":{"FilterCriteria":{"Filters":[{"Pattern":"{}"}]},`}, {"DescribePipe", `"TargetParameters":{`, `"Enrichment":"arn:aws:lambda:us-east-2:123456789012:function:other","TargetParameters":{`}, {"DescribePipe", "$.messageAttributes", "$.receiptHandle"}, {"DescribePipe", `"CloudWatchLogsParameters":{`, `"CloudWatchLogsParameters":{"Timestamp":"$.attributes.SentTimestamp",`},
		{"GetQueueAttributes", "1209600", "345600"}, {"GetQueueAttributes", "1800", "30"}, {"GetQueueAttributes", `"SqsManagedSseEnabled":"true"`, `"SqsManagedSseEnabled":"false"`}, {"GetQueueAttributes", `"ApproximateNumberOfMessages":"0"`, `"ApproximateNumberOfMessages":"1"`}, {"GetQueueAttributes", `"Attributes":{`, `"Attributes":{"Policy":"untrusted",`},
		{"DescribeLogStreams", "failures", "wrong"}, {"DescribeAlarms", "ScheduleGroup", "ScheduleName"}, {"DescribeAlarms", "FunctionName", "Resource"}, {"DescribeAlarms", "PipeName", "AwsAccountId"}, {"DescribeAlarms", `"StateValue":"OK"`, `"StateValue":"ALARM"`}, {"DescribeAlarms", `"TreatMissingData":"breaching"`, `"TreatMissingData":"notBreaching"`}, {"DescribeAlarms", `"EvaluationPeriods":3`, `"EvaluationPeriods":1`}, {"DescribeMetricFilters", `$.partial IS FALSE`, `$.partial IS TRUE`}, {"DescribeMetricFilters", `"MetricValue":"1"`, `"MetricValue":"0"`},
	} {
		t.Run(tc.method+tc.from, func(t *testing.T) {
			f, _, c, e, clients := evidenceFixture(t)
			before := f.responses[tc.method]
			f.responses[tc.method] = strings.ReplaceAll(before, tc.from, tc.to)
			if before == f.responses[tc.method] {
				t.Fatal("invalid mutation")
			}
			var err error
			if strings.Contains(tc.method, "Alarms") || tc.method == "DescribeMetricFilters" {
				err = checkEvidenceAlarms(context.Background(), clients, e, c)
			} else {
				err = checkEvidenceRoute(context.Background(), clients, e)
			}
			if err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}
func TestCompletionCannotBeInferredFromSilenceOrPartial(t *testing.T) {
	for _, mutate := range []func(*cleanuplambda.Record){func(r *cleanuplambda.Record) { r.OK = false }, func(r *cleanuplambda.Record) { r.Partial = true }, func(r *cleanuplambda.Record) { r.Deadline = true }, func(r *cleanuplambda.Record) { r.Result.ScanComplete = false }, func(r *cleanuplambda.Record) { r.Scope.Owner = "other" }, func(r *cleanuplambda.Record) { r.Result.Scope.Owner = "other" }, func(r *cleanuplambda.Record) { r.Correlation.ScheduleARN = "other" }, func(r *cleanuplambda.Record) { r.Result.Errors = []expiry.Problem{{Code: "root_volume_unverified"}} }, func(r *cleanuplambda.Record) { r.EmittedAt = "2026-09-14T11:00:00Z" }, func(r *cleanuplambda.Record) { r.Result.DryRun = true }} {
		f, m, c, _, clients := evidenceFixture(t)
		r := successfulRecord(m, c)
		mutate(&r)
		b, _ := json.Marshal(r)
		out, _ := json.Marshal(map[string]any{"Events": []any{map[string]string{"Message": string(b)}}})
		f.responses["FilterLogEvents"] = string(out)
		if checkRecentCompletion(context.Background(), f, m, c, clients.Now()) == nil {
			t.Fatal("invalid completion accepted", r)
		}
	}
	f, m, c, _, clients := evidenceFixture(t)
	f.responses["FilterLogEvents"] = `{"Events":[]}`
	if checkRecentCompletion(context.Background(), f, m, c, clients.Now()) == nil {
		t.Fatal("silence is not success")
	}
	f.fail = "FilterLogEvents"
	if err := checkRecentCompletion(context.Background(), f, m, c, clients.Now()); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal(err)
	}
}

func TestEvidenceDescriptorRejectsNegativeFixtures(t *testing.T) {
	for _, mutate := range []func(*config.CleanupEvidence){
		func(e *config.CleanupEvidence) { e.SchemaVersion = 2 }, func(e *config.CleanupEvidence) {
			e.Queue.ARN = strings.ReplaceAll(e.Queue.ARN, "123456789012", "999999999999")
		}, func(e *config.CleanupEvidence) { e.Queue.VisibilitySeconds = 30 }, func(e *config.CleanupEvidence) { e.Queue.RetentionSeconds = 3600 }, func(e *config.CleanupEvidence) { e.Queue.SSESQS = false }, func(e *config.CleanupEvidence) {
			e.Pipe.InputTemplate = strings.ReplaceAll(e.Pipe.InputTemplate, "<", `\u003c`)
		}, func(e *config.CleanupEvidence) { e.Pipe.DesiredState = "STOPPED" }, func(e *config.CleanupEvidence) { e.Logs.RetentionDays = 0 }, func(e *config.CleanupEvidence) { e.Stream = "other" }, func(e *config.CleanupEvidence) { e.HealthRole.ARN = e.PipeRole.ARN }, func(e *config.CleanupEvidence) { delete(e.Alarms, "no-success") }, func(e *config.CleanupEvidence) {
			e.DeliveryMetric.Dimensions = map[string]string{"ScheduleName": "wrong"}
		},
	} {
		_, m, c, e, _ := evidenceFixture(t)
		mutate(&e)
		raw, _ := json.Marshal(e)
		if _, err := config.DecodeEvidence(raw, m, c); err == nil {
			t.Fatal("invalid capability accepted", string(raw))
		}
	}
}

func TestBothProducerDestinationsAreVerified(t *testing.T) {
	for _, kind := range []string{"lambda", "scheduler"} {
		t.Run(kind, func(t *testing.T) {
			f, m, c, e, _ := evidenceFixture(t)
			var method string
			if kind == "lambda" {
				method = "GetFunctionEventInvokeConfig"
				f.responses[method] = `{"MaximumEventAgeInSeconds":300,"MaximumRetryAttempts":0,"DestinationConfig":{"OnFailure":{"Destination":"` + e.Queue.ARN + `"}}}`
			} else {
				method = "GetSchedule"
				var v map[string]any
				_ = json.Unmarshal([]byte(f.responses[method]), &v)
				v["Target"].(map[string]any)["DeadLetterConfig"] = map[string]string{"Arn": e.Queue.ARN}
				b, _ := json.Marshal(v)
				f.responses[method] = string(b)
			}
			check := func() error {
				if kind == "lambda" {
					return checkCleanupFunction(context.Background(), f, m, c)
				}
				return checkCleanupSchedule(context.Background(), f, c)
			}
			if err := check(); err != nil {
				t.Fatal("valid destination rejected", err)
			}
			f.responses[method] = strings.ReplaceAll(f.responses[method], e.Queue.ARN, e.Queue.ARN+"-other")
			if check() == nil {
				t.Fatal("wrong failure producer destination accepted")
			}
		})
	}
}

func TestCompleteCleanupHealthAndScopeMatchedRecovery(t *testing.T) {
	f, m, c, e, clients := evidenceFixture(t)
	c.Schedule.State = "ENABLED"
	m.Cleanup, _ = json.Marshal(c)
	f.responses["GetSchedule"] = strings.ReplaceAll(f.responses["GetSchedule"], "DISABLED", "ENABLED")
	var schedule map[string]any
	_ = json.Unmarshal([]byte(f.responses["GetSchedule"]), &schedule)
	schedule["Target"].(map[string]any)["DeadLetterConfig"] = map[string]string{"Arn": e.Queue.ARN}
	b, _ := json.Marshal(schedule)
	f.responses["GetSchedule"] = string(b)
	f.responses["GetFunctionEventInvokeConfig"] = `{"MaximumEventAgeInSeconds":300,"MaximumRetryAttempts":0,"DestinationConfig":{"OnFailure":{"Destination":"` + e.Queue.ARN + `"}}}`
	f.responses["DescribeLogGroups"] = `{"LogGroups":[{"LogGroupName":"` + c.Logs.Name + `","Arn":"` + c.Logs.ARN + `:*","RetentionInDays":30},{"LogGroupName":"` + e.Logs.Name + `","Arn":"` + e.Logs.ARN + `:*","RetentionInDays":30}]}`
	original := f.hook
	f.hook = func(method string, in any) (string, bool) {
		doc, ok := original(method, in)
		for _, r := range []config.Role{e.PipeRole, e.HealthRole} {
			doc = strings.ReplaceAll(doc, `"PolicyName":"`+roleName(r.ARN)+`"`, `"PolicyName":"`+r.PolicyName+`"`)
			doc = strings.ReplaceAll(doc, `"PolicyNames":["`+roleName(r.ARN)+`"]`, `"PolicyNames":["`+r.PolicyName+`"]`)
		}
		return doc, ok
	}
	for _, check := range VerifyCleanup(context.Background(), clients, m, m.Cleanup) {
		if check.Err != nil {
			t.Fatal(check.Name, check.Err)
		}
	}
	// An earlier success cannot hide the most recent failed completion.
	failed := successfulRecord(m, c)
	failed.EmittedAt = "2026-09-14T12:04:00Z"
	failed.OK = false
	failed.Complete = false
	failed.Partial = true
	failure, _ := json.Marshal(failed)
	success, _ := json.Marshal(successfulRecord(m, c))
	out, _ := json.Marshal(map[string]any{"Events": []any{map[string]string{"Message": string(success)}, map[string]string{"Message": string(failure)}}})
	f.responses["FilterLogEvents"] = string(out)
	if checkRecentCompletion(context.Background(), f, m, c, clients.Now()) == nil {
		t.Fatal("earlier success hid later failure")
	}
}

func TestUnsupportedEvidenceStillDiagnosesCoreSchedule(t *testing.T) {
	f, m, c := cleanupFixture(t)
	c.Evidence = json.RawMessage(`{"schema_version":99}`)
	raw, _ := json.Marshal(c)
	checks := VerifyCleanup(context.Background(), CleanupClients{Lambda: f, Scheduler: f, Logs: f, IAM: f}, m, raw)
	if len(checks) != 6 || checks[5].Name != "cleanup_evidence" || checks[5].Err == nil || !strings.Contains(checks[5].Err.Error(), "unsupported") || f.calls["GetSchedule"] != 1 {
		t.Fatal(checks, f.calls)
	}
}
