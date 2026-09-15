package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
)

// PipeInputTemplate is a literal AWS input template, not JSON until Pipes has
// substituted its values. Keep object placeholders unquoted and omit receipts.
const PipeInputTemplate = `{"schema_version":1,"kind":"invocation_failure","messageId":<$.messageId>,"body":<$.body>,"messageAttributes":<$.messageAttributes>,"SentTimestamp":<$.attributes.SentTimestamp>,"ingested_at":<aws.pipes.event.ingestion-time>,"pipe_arn":<aws.pipes.pipe-arn>,"source_arn":<aws.pipes.source-arn>}`

type CleanupEvidence struct {
	SchemaVersion  int                      `json:"schema_version"`
	Queue          EvidenceQueue            `json:"queue"`
	Pipe           EvidencePipe             `json:"pipe"`
	Logs           CleanupLogs              `json:"logs"`
	Stream         string                   `json:"stream"`
	PipeRole       Role                     `json:"pipe_role"`
	HealthRole     Role                     `json:"health_role"`
	SuccessFilter  EvidenceFilter           `json:"success_filter"`
	Alarms         map[string]EvidenceAlarm `json:"alarms"`
	DeliveryMetric EvidenceMetric           `json:"delivery_metric"`
}
type EvidenceQueue struct {
	Name              string `json:"name"`
	ARN               string `json:"arn"`
	URL               string `json:"url"`
	RetentionSeconds  int    `json:"retention_seconds"`
	VisibilitySeconds int    `json:"visibility_seconds"`
	SSESQS            bool   `json:"sse_sqs"`
}
type EvidencePipe struct {
	Name          string `json:"name"`
	ARN           string `json:"arn"`
	DesiredState  string `json:"desired_state"`
	BatchSize     int    `json:"batch_size"`
	InputTemplate string `json:"input_template"`
}
type EvidenceMetric struct {
	Namespace  string            `json:"namespace"`
	MetricName string            `json:"metric_name"`
	Dimensions map[string]string `json:"dimensions"`
}
type EvidenceFilter struct {
	Name       string `json:"name"`
	Pattern    string `json:"pattern"`
	Namespace  string `json:"namespace"`
	MetricName string `json:"metric_name"`
}
type EvidenceAlarm struct {
	EvidenceMetric
	Name               string  `json:"name"`
	ARN                string  `json:"arn"`
	Statistic          string  `json:"statistic"`
	Threshold          float64 `json:"threshold"`
	ComparisonOperator string  `json:"comparison_operator"`
	Period             int     `json:"period"`
	EvaluationPeriods  int     `json:"evaluation_periods"`
	TreatMissingData   string  `json:"treat_missing_data"`
}

func SuccessPattern(m Manifest) string {
	return fmt.Sprintf(`{ $.kind = "invocation_end" && $.ok IS TRUE && $.complete IS TRUE && $.partial IS FALSE && $.deadline IS FALSE && $.result.scan_complete IS TRUE && $.result.exit_code = 0 && $.result.dry_run IS FALSE && $.correlation.schedule_arn = "arn:aws:scheduler:%s:%s:schedule/devbox-%s-%s-cleanup/devbox-%s-%s-cleanup" && $.scope.account = "%s" && $.scope.region = "%s" && $.scope.deployment = "%s" && $.scope.owner = "%s" }`, m.Region, m.Account, m.Deployment, m.Owner, m.Deployment, m.Owner, m.Account, m.Region, m.Deployment, m.Owner)
}

// ExpectedEvidenceAlarms validates the exported operational contract as well as
// live settings. Counter silence alone never satisfies the completion alarm.
func ExpectedEvidenceAlarms(m Manifest, c Cleanup) map[string]EvidenceAlarm {
	out := map[string]EvidenceAlarm{}
	add := func(prefix, namespace, dimension, value string, names []string) {
		for _, metric := range names {
			key := prefix + "-" + metric
			a := EvidenceAlarm{EvidenceMetric: EvidenceMetric{namespace, metric, map[string]string{dimension: value}}, Statistic: "Sum", ComparisonOperator: "GreaterThanThreshold", Period: 60, EvaluationPeriods: 1, TreatMissingData: "notBreaching"}
			if prefix == "queue" {
				a.Statistic = "Maximum"
				if metric == "ApproximateAgeOfOldestMessage" {
					a.Threshold = 300
				}
			}
			out[key] = a
		}
	}
	name := "devbox-" + m.Deployment + "-" + m.Owner + "-evidence"
	add("scheduler", "AWS/Scheduler", "ScheduleGroup", c.Schedule.GroupName, []string{"TargetErrorCount", "InvocationDroppedCount", "InvocationsFailedToBeSentToDeadLetterCount"})
	add("lambda", "AWS/Lambda", "FunctionName", c.Schedule.Name, []string{"Errors", "Throttles", "AsyncEventsDropped", "DestinationDeliveryFailures"})
	add("pipe", "AWS/Pipes", "PipeName", name, []string{"ExecutionFailed", "ExecutionTimeout", "ExecutionPartiallyFailed", "TargetStageFailed"})
	add("queue", "AWS/SQS", "QueueName", name, []string{"ApproximateNumberOfMessagesVisible", "ApproximateNumberOfMessagesNotVisible", "ApproximateAgeOfOldestMessage"})
	out["scheduler-InvocationAttemptCount"] = EvidenceAlarm{EvidenceMetric: EvidenceMetric{"AWS/Scheduler", "InvocationAttemptCount", map[string]string{"ScheduleGroup": c.Schedule.GroupName}}, Statistic: "Sum", Threshold: 1, ComparisonOperator: "LessThanThreshold", Period: 300, EvaluationPeriods: 3, TreatMissingData: "breaching"}
	out["no-success"] = EvidenceAlarm{EvidenceMetric: EvidenceMetric{"Devbox/Cleanup/" + c.Schedule.Name, "SuccessfulCompletion", map[string]string{}}, Statistic: "Sum", Threshold: 1, ComparisonOperator: "LessThanThreshold", Period: 300, EvaluationPeriods: 3, TreatMissingData: "breaching"}
	for key, a := range out {
		a.Name = c.Schedule.Name + "-" + key
		a.ARN = "arn:aws:cloudwatch:" + m.Region + ":" + m.Account + ":alarm:" + a.Name
		out[key] = a
	}
	return out
}

func DecodeEvidence(raw json.RawMessage, m Manifest, c Cleanup) (CleanupEvidence, error) {
	var e CleanupEvidence
	fail := errors.New("cleanup evidence unsupported or invalid; install and export the reviewed evidence capability")
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF {
		return e, fail
	}
	name := "devbox-" + m.Deployment + "-" + m.Owner
	regional := m.Region + ":" + m.Account
	if e.SchemaVersion != 1 ||
		e.Queue != (EvidenceQueue{name + "-evidence", "arn:aws:sqs:" + regional + ":" + name + "-evidence", "https://sqs." + m.Region + ".amazonaws.com/" + m.Account + "/" + name + "-evidence", 1209600, 1800, true}) ||
		e.Pipe != (EvidencePipe{name + "-evidence", "arn:aws:pipes:" + regional + ":pipe/" + name + "-evidence", "RUNNING", 1, PipeInputTemplate}) {
		return e, fail
	}
	logName := "/devbox/" + name + "/failures"
	if e.Logs != (CleanupLogs{logName, "arn:aws:logs:" + regional + ":log-group:" + logName, c.Logs.RetentionDays}) || e.Stream != "failures" {
		return e, fail
	}
	for suffix, r := range map[string]Role{"evidence": e.PipeRole, "health": e.HealthRole} {
		if r.ARN != "arn:aws:iam::"+m.Account+":role/"+name+"-"+suffix || r.PolicyName != "devbox-"+suffix || !digestRE.MatchString(r.TrustSHA256) || !digestRE.MatchString(r.PolicySHA256) {
			return e, fail
		}
	}
	if e.SuccessFilter != (EvidenceFilter{"successful-completion", SuccessPattern(m), "Devbox/Cleanup/" + c.Schedule.Name, "SuccessfulCompletion"}) ||
		!reflect.DeepEqual(e.Alarms, ExpectedEvidenceAlarms(m, c)) ||
		!reflect.DeepEqual(e.DeliveryMetric, EvidenceMetric{"AWS/Scheduler", "InvocationAttemptCount", map[string]string{"ScheduleGroup": c.Schedule.GroupName}}) {
		return e, fail
	}
	return e, nil
}
