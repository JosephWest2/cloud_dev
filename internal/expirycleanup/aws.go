package expirycleanup

import (
	"context"
	"errors"
	"strings"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/logging"
)

// NewAWS constructs both clients from the same credential source and explicit
// region. The adapter loads aws.Config within its own bounded context. No launch
// configuration or infrastructure health is needed. Custom endpoints in cfg are
// trusted wiring, never resource or schedule input.
func NewAWS(scope expiry.Scope, cfg aws.Config, clock expiry.Clock, sink expiry.Sink, limits Limits) (*Service, error) {
	if cfg.Region != scope.Region || cfg.Region == "" || isNil(cfg.Credentials) {
		return nil, failure("cleanup_invalid")
	}
	cfg.Logger = logging.Nop{}
	cfg.ClientLogMode = 0
	return New(scope, Dependencies{EC2: ec2.NewFromConfig(cfg), STS: sts.NewFromConfig(cfg), Clock: clock, Sink: sink}, limits)
}
func (s *Service) reads(o *ec2.Options) {
	o.Region = s.scope.Region
	o.RetryMaxAttempts = s.limits.ReadAttempts
	o.Retryer = retry.NewStandard(func(r *retry.StandardOptions) { r.MaxAttempts = s.limits.ReadAttempts })
}
func (s *Service) mutation(o *ec2.Options) {
	o.Region = s.scope.Region
	o.RetryMaxAttempts = 1
	o.Retryer = aws.NopRetryer{}
}
func (s *Service) verify(ctx context.Context) error {
	request, cancel := context.WithTimeout(ctx, s.limits.RequestTimeout)
	defer cancel()
	out, err := s.deps.STS.GetCallerIdentity(request, &sts.GetCallerIdentityInput{}, func(o *sts.Options) {
		o.Region = s.scope.Region
		o.RetryMaxAttempts = s.limits.ReadAttempts
		o.Retryer = retry.NewStandard(func(r *retry.StandardOptions) { r.MaxAttempts = s.limits.ReadAttempts })
	})
	if err != nil || request.Err() != nil || out == nil || aws.ToString(out.Account) != s.scope.Account {
		return failure("identity_unverified")
	}
	// Account is the authorization boundary. If STS also returns an ARN, reject
	// contradictory or malformed identity evidence without exposing the principal.
	if out.Arn != nil {
		a, err := arn.Parse(*out.Arn)
		if err != nil || a.AccountID != s.scope.Account || a.Partition != partition(s.scope.Region) || (a.Service != "sts" && a.Service != "iam") {
			return failure("identity_unverified")
		}
	}
	return nil
}
func apiCode(err error, codes ...string) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	for _, code := range codes {
		if api.ErrorCode() == code {
			return true
		}
	}
	return false
}
func (s *Service) validZone(zone string) bool {
	if !strings.HasPrefix(zone, s.scope.Region) {
		return false
	}
	tail := strings.TrimPrefix(zone, s.scope.Region)
	// Standard AZs plus AWS Local/Wavelength zone suffixes; require the region
	// boundary so us-east-10a cannot be mistaken for us-east-1.
	return zoneSuffix.MatchString(tail)
}
func (s *Service) describeVolumes(ctx context.Context, id string) (*ec2.DescribeVolumesOutput, error) {
	request, cancel := context.WithTimeout(ctx, s.limits.RequestTimeout)
	defer cancel()
	out, err := s.deps.EC2.DescribeVolumes(request, &ec2.DescribeVolumesInput{VolumeIds: []string{id}}, s.reads)
	if request.Err() != nil {
		return out, request.Err()
	}
	return out, err
}
func (s *Service) exactVolume(out *ec2.DescribeVolumesOutput, id string) bool {
	if out == nil || aws.ToString(out.NextToken) != "" || len(out.Volumes) != 1 || aws.ToString(out.Volumes[0].VolumeId) != id {
		return false
	}
	v := out.Volumes[0]
	if v.OwnerId != nil && aws.ToString(v.OwnerId) != s.scope.Account {
		return false
	}
	if v.AvailabilityZone != nil && !s.validZone(*v.AvailabilityZone) {
		return false
	}
	if v.VolumeArn != nil {
		a, err := arn.Parse(*v.VolumeArn)
		if err != nil || a.Partition != partition(s.scope.Region) || a.Service != "ec2" || a.Region != s.scope.Region || a.AccountID != s.scope.Account || a.Resource != "volume/"+id {
			return false
		}
	}
	for _, a := range v.Attachments {
		if a.VolumeId != nil && *a.VolumeId != id {
			return false
		}
		if v.State == "deleted" && a.State != "detached" {
			return false
		}
	}
	return true
}
func partition(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "aws-cn"
	case strings.HasPrefix(region, "us-gov-"):
		return "aws-us-gov"
	case strings.HasPrefix(region, "us-isob-"):
		return "aws-iso-b"
	case strings.HasPrefix(region, "us-iso-"):
		return "aws-iso"
	case strings.HasPrefix(region, "eu-isoe-"):
		return "aws-iso-e"
	case strings.HasPrefix(region, "us-isof-"):
		return "aws-iso-f"
	case strings.HasPrefix(region, "eusc-"):
		return "aws-eusc"
	}
	return "aws"
}
