package foundation

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
)

type CleanupLambda interface {
	GetFunctionConfiguration(context.Context, *lambda.GetFunctionConfigurationInput, ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error)
	GetFunctionConcurrency(context.Context, *lambda.GetFunctionConcurrencyInput, ...func(*lambda.Options)) (*lambda.GetFunctionConcurrencyOutput, error)
	GetFunctionEventInvokeConfig(context.Context, *lambda.GetFunctionEventInvokeConfigInput, ...func(*lambda.Options)) (*lambda.GetFunctionEventInvokeConfigOutput, error)
}
type CleanupScheduler interface {
	GetSchedule(context.Context, *scheduler.GetScheduleInput, ...func(*scheduler.Options)) (*scheduler.GetScheduleOutput, error)
}
type CleanupLogs interface {
	DescribeLogGroups(context.Context, *cloudwatchlogs.DescribeLogGroupsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
}
type CleanupClients struct {
	Lambda       CleanupLambda
	Scheduler    CleanupScheduler
	Logs         CleanupLogs
	IAM          IAM
	Pipe         EvidencePipe
	Queue        EvidenceQueue
	CloudWatch   EvidenceCloudWatch
	EvidenceLogs EvidenceLogs
	Now          func() time.Time
}

// VerifyCleanup is a dedicated read-only doctor capability, never a prerequisite
// of the shared cleanup service, inventory, explicit down or durable log reads.
func VerifyCleanup(ctx context.Context, clients CleanupClients, m config.Manifest, raw json.RawMessage) []Check {
	c, err := config.DecodeCleanup(raw, m)
	if err != nil {
		return []Check{{"cleanup_configuration", err}}
	}
	checks := []Check{}
	for _, probe := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"cleanup_function", func(ctx context.Context) error { return checkCleanupFunction(ctx, clients.Lambda, m, c) }},
		{"cleanup_schedule", func(ctx context.Context) error { return checkCleanupSchedule(ctx, clients.Scheduler, c) }},
		{"cleanup_logs", func(ctx context.Context) error { return checkCleanupLogs(ctx, clients.Logs, c) }},
		{"cleanup_iam", func(ctx context.Context) error { return checkCleanupRoles(ctx, clients.IAM, m, c) }},
	} {
		request, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := probe.run(request)
		if request.Err() != nil {
			err = request.Err()
		}
		cancel()
		checks = append(checks, Check{probe.name, err})
	}
	// A descriptor or successful deployment is never proof of recent execution.
	if c.Schedule.State != "ENABLED" {
		checks = append(checks, Check{"cleanup_enabled", errors.New("cleanup schedule disabled")})
	}
	checks = append(checks, verifyEvidence(ctx, clients, m, c)...)
	return checks
}
func checkCleanupFunction(ctx context.Context, api CleanupLambda, m config.Manifest, c config.Cleanup) error {
	fail := errors.New("cleanup function configuration drift")
	if api == nil {
		return fail
	}
	f := c.Function
	out, err := api.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(f.ARN)})
	if err != nil || out == nil {
		return fail
	}
	hash, err := base64.StdEncoding.DecodeString(aws.ToString(out.CodeSha256))
	env := map[string]string{"DEVBOX_ACCOUNT": m.Account, "DEVBOX_REGION": m.Region, "DEVBOX_DEPLOYMENT": m.Deployment, "DEVBOX_OWNER": m.Owner, "DEVBOX_LOG_GROUP": c.Logs.Name}
	envJSON, _ := json.Marshal(env)
	if err != nil || hex.EncodeToString(hash) != f.CodeSHA256 || string(out.Runtime) != f.Runtime || len(out.Architectures) != 1 || string(out.Architectures[0]) != f.Architecture || aws.ToString(out.FunctionArn) != f.ARN || aws.ToString(out.Role) != c.ExecutionRole.ARN || aws.ToString(out.Handler) != "bootstrap" || int(aws.ToInt32(out.Timeout)) != f.TimeoutSeconds || int(aws.ToInt32(out.MemorySize)) != f.MemoryMB || out.State != "Active" || out.LastUpdateStatus != "Successful" || out.Environment == nil || out.Environment.Error != nil || !reflect.DeepEqual(out.Environment.Variables, env) || digest(envJSON) != f.EnvironmentSHA256 || out.VpcConfig != nil && len(out.VpcConfig.SubnetIds) > 0 || out.Layers != nil && len(out.Layers) > 0 {
		return fail
	}
	concurrency, err := api.GetFunctionConcurrency(ctx, &lambda.GetFunctionConcurrencyInput{FunctionName: aws.String(f.ARN)})
	if err != nil || concurrency == nil || int(aws.ToInt32(concurrency.ReservedConcurrentExecutions)) != f.ReservedConcurrency {
		return fail
	}
	async, err := api.GetFunctionEventInvokeConfig(ctx, &lambda.GetFunctionEventInvokeConfigInput{FunctionName: aws.String(f.ARN)})
	if err != nil || async == nil || int(aws.ToInt32(async.MaximumEventAgeInSeconds)) != f.AsyncMaxAgeSeconds || async.MaximumRetryAttempts == nil || int(*async.MaximumRetryAttempts) != f.AsyncRetryAttempts {
		return fail
	}
	if e, err := config.DecodeEvidence(c.Evidence, m, c); err == nil {
		if async.DestinationConfig == nil || async.DestinationConfig.OnFailure == nil || aws.ToString(async.DestinationConfig.OnFailure.Destination) != e.Queue.ARN || async.DestinationConfig.OnSuccess != nil && aws.ToString(async.DestinationConfig.OnSuccess.Destination) != "" {
			return fail
		}
	}
	return nil
}
func checkCleanupSchedule(ctx context.Context, api CleanupScheduler, c config.Cleanup) error {
	fail := errors.New("cleanup schedule configuration drift")
	if api == nil {
		return fail
	}
	s := c.Schedule
	out, err := api.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(s.Name), GroupName: aws.String(s.GroupName)})
	if err != nil || out == nil || aws.ToString(out.Arn) != s.ARN || aws.ToString(out.Name) != s.Name || aws.ToString(out.GroupName) != s.GroupName || string(out.State) != s.State || aws.ToString(out.ScheduleExpression) != s.Expression || out.FlexibleTimeWindow == nil || string(out.FlexibleTimeWindow.Mode) != s.FlexibleWindow || out.StartDate != nil || out.EndDate != nil || out.Target == nil {
		return fail
	}
	t := out.Target
	hash, err := jsonDigest(aws.ToString(t.Input))
	if err != nil || hash != s.InputSHA256 || aws.ToString(t.Arn) != c.Function.ARN || aws.ToString(t.RoleArn) != c.SchedulerRole.ARN || t.RetryPolicy == nil || int(aws.ToInt32(t.RetryPolicy.MaximumEventAgeInSeconds)) != s.MaxAgeSeconds || t.RetryPolicy.MaximumRetryAttempts == nil || int(*t.RetryPolicy.MaximumRetryAttempts) != s.RetryAttempts {
		return fail
	}
	var e config.CleanupEvidence
	if json.Unmarshal(c.Evidence, &e) == nil && e.SchemaVersion == 1 && e.Queue.ARN != "" {
		if t.DeadLetterConfig == nil || aws.ToString(t.DeadLetterConfig.Arn) != e.Queue.ARN {
			return fail
		}
	}
	return nil
}
func checkCleanupLogs(ctx context.Context, api CleanupLogs, c config.Cleanup) error {
	fail := errors.New("cleanup retained logs drift")
	if api == nil {
		return fail
	}
	var token *string
	seen := map[string]bool{}
	for page := 0; page < 16; page++ {
		out, err := api.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(c.Logs.Name), NextToken: token, Limit: aws.Int32(50)})
		if err != nil || out == nil {
			return fail
		}
		for _, g := range out.LogGroups {
			if aws.ToString(g.LogGroupName) == c.Logs.Name {
				if aws.ToString(g.Arn) != c.Logs.ARN+":*" || int(aws.ToInt32(g.RetentionInDays)) != c.Logs.RetentionDays {
					return fail
				}
				return nil
			}
		}
		next := aws.ToString(out.NextToken)
		if next == "" || seen[next] {
			return fail
		}
		seen[next] = true
		token = out.NextToken
	}
	return fail
}

func checkCleanupRoles(ctx context.Context, api IAM, m config.Manifest, c config.Cleanup) error {
	for _, role := range []config.Role{c.ExecutionRole, c.SchedulerRole} {
		if err := checkRole(ctx, api, m, role); err != nil {
			return err
		}
	}
	return nil
}
