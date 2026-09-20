package foundation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/cleanuplambda"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/pipes"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type EvidencePipe interface {
	DescribePipe(context.Context, *pipes.DescribePipeInput, ...func(*pipes.Options)) (*pipes.DescribePipeOutput, error)
}
type EvidenceQueue interface {
	GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}
type EvidenceCloudWatch interface {
	DescribeAlarms(context.Context, *cloudwatch.DescribeAlarmsInput, ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error)
}
type EvidenceLogs interface {
	FilterLogEvents(context.Context, *cloudwatchlogs.FilterLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error)
	DescribeMetricFilters(context.Context, *cloudwatchlogs.DescribeMetricFiltersInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeMetricFiltersOutput, error)
	DescribeLogStreams(context.Context, *cloudwatchlogs.DescribeLogStreamsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error)
}

func verifyEvidence(ctx context.Context, clients CleanupClients, m config.Manifest, c config.Cleanup) []Check {
	return verifyEvidenceMode(ctx, clients, m, c, false)
}

func verifyEvidenceMode(ctx context.Context, clients CleanupClients, m config.Manifest, c config.Cleanup, configurationOnly bool) []Check {
	e, err := config.DecodeEvidence(c.Evidence, m, c)
	if err != nil {
		return []Check{{"cleanup_evidence", err}}
	}
	checks := []Check{}
	for _, probe := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"cleanup_evidence_route", func(ctx context.Context) error { return checkEvidenceRoute(ctx, clients, e) }},
		{"cleanup_evidence_logs", func(ctx context.Context) error {
			copy := c
			copy.Logs = e.Logs
			return checkCleanupLogs(ctx, clients.Logs, copy)
		}},
		{"cleanup_evidence_iam", func(ctx context.Context) error {
			for _, r := range []config.Role{e.PipeRole, e.HealthRole} {
				if err := checkRole(ctx, clients.IAM, m, r); err != nil {
					return err
				}
			}
			return nil
		}},
		{"cleanup_evidence_alarms", func(ctx context.Context) error { return checkEvidenceAlarmsMode(ctx, clients, e, c, configurationOnly) }},
		{"cleanup_recent_completion", func(ctx context.Context) error {
			now := time.Now()
			if clients.Now != nil {
				now = clients.Now()
			}
			return checkRecentCompletionAfter(ctx, clients.EvidenceLogs, m, c, now, clients.CompletedAfter)
		}},
	} {
		if configurationOnly && probe.name == "cleanup_recent_completion" {
			continue
		}
		bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := probe.run(bounded)
		if bounded.Err() != nil {
			err = bounded.Err()
		}
		cancel()
		checks = append(checks, Check{probe.name, err})
	}
	return checks
}
func checkEvidenceRoute(ctx context.Context, clients CleanupClients, e config.CleanupEvidence) error {
	fail := errors.New("cleanup failure route drift or backlog; inspect Pipe currentState/StateReason, queue attributes and exact log stream")
	if clients.Pipe == nil || clients.Queue == nil || clients.EvidenceLogs == nil {
		return fail
	}
	p, err := clients.Pipe.DescribePipe(ctx, &pipes.DescribePipeInput{Name: aws.String(e.Pipe.Name)})
	if err != nil ||
		p == nil ||
		aws.ToString(p.Arn) != e.Pipe.ARN ||
		aws.ToString(p.Name) != e.Pipe.Name ||
		string(p.CurrentState) != "RUNNING" ||
		string(p.DesiredState) != e.Pipe.DesiredState ||
		aws.ToString(p.RoleArn) != e.PipeRole.ARN ||
		aws.ToString(p.Source) != e.Queue.ARN ||
		aws.ToString(p.Target) != e.Logs.ARN ||
		aws.ToString(p.Enrichment) != "" ||
		// DescribePipe can return {} for an unused enrichment. An empty input
		// template also means removal; any HTTP configuration remains drift.
		(p.EnrichmentParameters != nil && (p.EnrichmentParameters.HttpParameters != nil || aws.ToString(p.EnrichmentParameters.InputTemplate) != "")) ||
		p.SourceParameters == nil ||
		p.SourceParameters.FilterCriteria != nil ||
		p.SourceParameters.SqsQueueParameters == nil ||
		aws.ToInt32(p.SourceParameters.SqsQueueParameters.BatchSize) != 1 ||
		aws.ToInt32(p.SourceParameters.SqsQueueParameters.MaximumBatchingWindowInSeconds) != 0 ||
		p.TargetParameters == nil ||
		aws.ToString(p.TargetParameters.InputTemplate) != e.Pipe.InputTemplate ||
		p.TargetParameters.CloudWatchLogsParameters == nil ||
		aws.ToString(p.TargetParameters.CloudWatchLogsParameters.LogStreamName) != e.Stream ||
		aws.ToString(p.TargetParameters.CloudWatchLogsParameters.Timestamp) != "" {
		return fail
	}
	q, err := clients.Queue.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(e.Queue.URL), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll}})
	if err != nil || q == nil {
		return fail
	}
	for key, want := range map[string]string{"QueueArn": e.Queue.ARN, "MessageRetentionPeriod": "1209600", "VisibilityTimeout": "1800", "SqsManagedSseEnabled": "true", "ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "0", "ApproximateNumberOfMessagesDelayed": "0"} {
		if q.Attributes[key] != want {
			return fail
		}
	}
	for _, key := range []string{"Policy", "RedrivePolicy", "RedriveAllowPolicy", "KmsMasterKeyId"} {
		if q.Attributes[key] != "" {
			return fail
		}
	}
	if q.Attributes["FifoQueue"] != "" && q.Attributes["FifoQueue"] != "false" {
		return fail
	}
	streams, err := clients.EvidenceLogs.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{LogGroupName: aws.String(e.Logs.Name), LogStreamNamePrefix: aws.String(e.Stream), Limit: aws.Int32(50)})
	if err != nil || streams == nil {
		return fail
	}
	for _, s := range streams.LogStreams {
		if aws.ToString(s.LogStreamName) == e.Stream && aws.ToString(s.Arn) == e.Logs.ARN+":log-stream:"+e.Stream {
			return nil
		}
	}
	return fail
}
func checkEvidenceAlarms(ctx context.Context, clients CleanupClients, e config.CleanupEvidence, c config.Cleanup) error {
	return checkEvidenceAlarmsMode(ctx, clients, e, c, false)
}

func checkEvidenceAlarmsMode(ctx context.Context, clients CleanupClients, e config.CleanupEvidence, c config.Cleanup, configurationOnly bool) error {
	fail := errors.New("cleanup alarm/filter drift or alarm not OK; inspect exported alarms and recent successful completion")
	if clients.CloudWatch == nil || clients.EvidenceLogs == nil {
		return fail
	}
	names := []string{}
	byName := map[string]config.EvidenceAlarm{}
	for _, a := range e.Alarms {
		names = append(names, a.Name)
		byName[a.Name] = a
	}
	slices.Sort(names)
	out, err := clients.CloudWatch.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: names})
	if err != nil || out == nil || out.NextToken != nil || len(out.CompositeAlarms) != 0 || len(out.MetricAlarms) != len(names) {
		return fail
	}
	seen := map[string]bool{}
	for _, a := range out.MetricAlarms {
		name := aws.ToString(a.AlarmName)
		want, ok := byName[name]
		if !ok || seen[name] {
			return fail
		}
		seen[name] = true
		dimensions := map[string]string{}
		for _, d := range a.Dimensions {
			dimensions[aws.ToString(d.Name)] = aws.ToString(d.Value)
		}
		if aws.ToString(a.AlarmArn) != want.ARN ||
			(!configurationOnly && string(a.StateValue) != "OK") ||
			aws.ToString(a.Namespace) != want.Namespace ||
			aws.ToString(a.MetricName) != want.MetricName ||
			len(dimensions) != len(a.Dimensions) ||
			!reflect.DeepEqual(dimensions, want.Dimensions) ||
			string(a.Statistic) != want.Statistic ||
			aws.ToFloat64(a.Threshold) != want.Threshold ||
			string(a.ComparisonOperator) != want.ComparisonOperator ||
			int(aws.ToInt32(a.Period)) != want.Period ||
			int(aws.ToInt32(a.EvaluationPeriods)) != want.EvaluationPeriods ||
			int(aws.ToInt32(a.DatapointsToAlarm)) != want.EvaluationPeriods ||
			aws.ToString(a.TreatMissingData) != want.TreatMissingData ||
			len(a.Metrics) != 0 ||
			aws.ToString(a.ThresholdMetricId) != "" ||
			a.ExtendedStatistic != nil {
			return fail
		}
	}
	filters, err := clients.EvidenceLogs.DescribeMetricFilters(ctx, &cloudwatchlogs.DescribeMetricFiltersInput{LogGroupName: aws.String(c.Logs.Name), FilterNamePrefix: aws.String(e.SuccessFilter.Name)})
	if err != nil || filters == nil || filters.NextToken != nil {
		return fail
	}
	for _, f := range filters.MetricFilters {
		if aws.ToString(f.FilterName) != e.SuccessFilter.Name {
			continue
		}
		if aws.ToString(f.LogGroupName) != c.Logs.Name || aws.ToString(f.FilterPattern) != e.SuccessFilter.Pattern || len(f.MetricTransformations) != 1 || f.ApplyOnTransformedLogs || len(f.EmitSystemFieldDimensions) != 0 || aws.ToString(f.FieldSelectionCriteria) != "" {
			return fail
		}
		mt := f.MetricTransformations[0]
		if aws.ToString(mt.MetricName) != e.SuccessFilter.MetricName || aws.ToString(mt.MetricNamespace) != e.SuccessFilter.Namespace || aws.ToString(mt.MetricValue) != "1" || mt.DefaultValue != nil || len(mt.Dimensions) != 0 || string(mt.Unit) != "Count" {
			return fail
		}
		return nil
	}
	return fail
}
func checkRecentCompletion(ctx context.Context, api EvidenceLogs, m config.Manifest, c config.Cleanup, now time.Time) error {
	return checkRecentCompletionAfter(ctx, api, m, c, now, time.Time{})
}

func checkRecentCompletionAfter(ctx context.Context, api EvidenceLogs, m config.Manifest, c config.Cleanup, now, after time.Time) error {
	fail := errors.New("no recent successful nonpartial scope-matched cleanup completion; inspect invocation_end and run manual cleanup if needed")
	if api == nil {
		return fail
	}
	var token *string
	seen := map[string]bool{}
	var latest *cleanuplambda.Record
	var latestAt time.Time
	for page := 0; page < 16; page++ {
		out, err := api.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(c.Logs.Name), StartTime: aws.Int64(now.Add(-15 * time.Minute).UnixMilli()), EndTime: aws.Int64(now.UnixMilli()), FilterPattern: aws.String(`{ $.kind = "invocation_end" }`), NextToken: token, Limit: aws.Int32(100)})
		if err != nil || out == nil {
			return fail
		}
		for _, event := range out.Events {
			var r cleanuplambda.Record
			if json.Unmarshal([]byte(aws.ToString(event.Message)), &r) != nil || r.Kind != "invocation_end" {
				return fail
			}
			at, err := expiry.ParseTimestamp(r.EmittedAt)
			if err != nil || at.After(now) || at.Before(now.Add(-15*time.Minute)) {
				return fail
			}
			if latest == nil || !at.Before(latestAt) {
				copy := r
				latest = &copy
				latestAt = at
			}
		}
		next := aws.ToString(out.NextToken)
		if next == "" {
			break
		}
		if seen[next] || page == 15 {
			return fail
		}
		seen[next] = true
		token = out.NextToken
	}
	if latest == nil {
		return fail
	}
	r := latest
	scope := expiry.Scope{Account: m.Account, Region: m.Region, Deployment: m.Deployment, Owner: m.Owner}
	if r.SchemaVersion != 1 || r.RunID == "" || r.RunID != r.RequestID || r.Scope != scope || !r.OK || !r.Complete || r.Partial || r.Deadline || r.Correlation.ScheduleARN != c.Schedule.ARN || r.Correlation.ExecutionID == "" || r.Result == nil {
		return fail
	}
	result := r.Result
	evaluated, err := expiry.ParseTimestamp(result.EvaluatedAt)
	if err != nil || evaluated.After(latestAt) || evaluated.Before(now.Add(-15*time.Minute)) {
		return fail
	}
	completed, err := expiry.ParseTimestamp(result.CompletedAt)
	if err != nil || completed.Before(evaluated) || completed.After(latestAt) {
		return fail
	}
	if _, err := strconv.Atoi(r.Correlation.AttemptNumber); err != nil {
		return fail
	}
	if scheduled, err := expiry.ParseTimestamp(r.Correlation.ScheduledTime); err != nil || (!after.IsZero() && !scheduled.After(after)) {
		return fail
	}
	if result.Scope != scope || !result.OK || !result.Complete || !result.ScanComplete || result.DryRun || result.ExitCode != 0 || len(result.Errors) != 0 || !slices.Contains([]string{"cleanup_complete", "cleanup_no_candidates"}, result.Code) || result.Code != r.Code {
		return fail
	}
	return nil
}
