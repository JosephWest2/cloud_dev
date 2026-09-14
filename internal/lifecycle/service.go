// Package lifecycle allocates and removes individual instances within a verified scope.
package lifecycle

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/foundation"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

type EC2 interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	RunInstances(context.Context, *ec2.RunInstancesInput, ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
}

type Service struct {
	API              EC2
	Fleet            FleetAPI
	LaunchRecords    S3LaunchAPI
	SSM              SSM
	Scope            config.Config
	VerifyFoundation func(context.Context, config.Manifest, config.Profile) error
	PollInterval     time.Duration
}

// New verifies STS before constructing any resource service. All clients share
// the same credential cache and explicit regional configuration.
func New(ctx context.Context, c config.Config) (*Service, error) {
	a, err := identity.Load(ctx, c)
	if err != nil {
		return nil, err
	}
	if err = identity.Verify(ctx, sts.NewFromConfig(a), c.ExpectedAccount); err != nil {
		return nil, err
	}
	client := ec2.NewFromConfig(a)
	return &Service{API: client, Fleet: client, LaunchRecords: s3.NewFromConfig(a), SSM: ssm.NewFromConfig(a), Scope: c, VerifyFoundation: func(ctx context.Context, m config.Manifest, p config.Profile) error {
		for _, check := range foundation.Verify(ctx, foundation.Clients{EC2: client, IAM: iam.NewFromConfig(a), SSM: ssm.NewFromConfig(a), S3: s3.NewFromConfig(a)}, m, p) {
			if check.Err != nil {
				return failure("foundation_drift", foundation.Message(check.Name))
			}
		}
		return nil
	}}, nil
}

type Failure struct{ Code, Message string }

func (f *Failure) Error() string         { return f.Message }
func failure(code, message string) error { return &Failure{code, message} }
func apiCode(err error, code string) bool {
	var a smithy.APIError
	return errors.As(err, &a) && a.ErrorCode() == code
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
var instanceRE = regexp.MustCompile(`^i-([0-9a-f]{8}|[0-9a-f]{17})$`)
var commandRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var volumeRE = regexp.MustCompile(`^vol-([0-9a-f]{8}|[0-9a-f]{17})$`)

func ValidName(s string) bool   { return nameRE.MatchString(s) && !instanceRE.MatchString(s) }
func ValidTarget(s string) bool { return instanceRE.MatchString(s) || ValidName(s) }

func (s *Service) pause(ctx context.Context, attempt int) error {
	d := s.PollInterval
	if d == 0 {
		d = time.Second << min(attempt, 3)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
