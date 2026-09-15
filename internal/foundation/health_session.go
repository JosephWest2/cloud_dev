package foundation

import (
	"context"
	"errors"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/pipes"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type healthSTS interface {
	AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

// Assume once for this bounded doctor call. Do not install refreshing credentials
// into the operator config or any emergency cleanup, down or results client.
func healthConfig(ctx context.Context, api healthSTS, operatorIAM IAM, a aws.Config, m config.Manifest, e config.CleanupEvidence) (aws.Config, error) {
	fail := errors.New("cleanup health role unavailable; verify its exported policy/trust and operator AssumeRole grant")
	request, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := checkRole(request, operatorIAM, m, e.HealthRole); err != nil {
		return aws.Config{}, fail
	}
	out, err := api.AssumeRole(request, &sts.AssumeRoleInput{RoleArn: aws.String(e.HealthRole.ARN), RoleSessionName: aws.String("devbox-cleanup-health"), DurationSeconds: aws.Int32(900)})
	if err != nil || request.Err() != nil || out == nil || out.Credentials == nil {
		return aws.Config{}, fail
	}
	c := out.Credentials
	if aws.ToString(c.AccessKeyId) == "" || aws.ToString(c.SecretAccessKey) == "" || aws.ToString(c.SessionToken) == "" || c.Expiration == nil || !c.Expiration.After(time.Now()) {
		return aws.Config{}, fail
	}
	a.Credentials = credentials.NewStaticCredentialsProvider(*c.AccessKeyId, *c.SecretAccessKey, *c.SessionToken)
	return a, nil
}
func checkCleanupDeployment(ctx context.Context, a aws.Config, m config.Manifest) []Check {
	c, err := config.DecodeCleanup(m.Cleanup, m)
	if err != nil {
		return []Check{{"cleanup_configuration", err}}
	}
	e, err := config.DecodeEvidence(c.Evidence, m, c)
	if err != nil {
		return []Check{{"cleanup_evidence", err}}
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	health, err := healthConfig(bounded, sts.NewFromConfig(a), iam.NewFromConfig(a), a, m, e)
	if err != nil {
		return []Check{{"cleanup_health_access", err}}
	}
	logs := cloudwatchlogs.NewFromConfig(health)
	return VerifyCleanup(bounded, CleanupClients{Lambda: lambda.NewFromConfig(health), Scheduler: scheduler.NewFromConfig(health), Logs: logs, IAM: iam.NewFromConfig(health), Pipe: pipes.NewFromConfig(health), Queue: sqs.NewFromConfig(health), CloudWatch: cloudwatch.NewFromConfig(health), EvidenceLogs: logs}, m, m.Cleanup)
}
