package setup

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/foundation"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

type AWSCloud struct{}

func samePrincipal(caller, principal, session string) bool {
	if strings.Contains(principal, ":user/") {
		return caller == principal && session == ""
	}
	prefix, role, ok := strings.Cut(principal, ":role/")
	if !ok {
		return false
	}
	name := role[strings.LastIndex(role, "/")+1:]
	expected := strings.Replace(prefix, ":iam:", ":sts:", 1) + ":assumed-role/" + name + "/"
	if !strings.HasPrefix(caller, expected) {
		return false
	}
	suffix := strings.TrimPrefix(caller, expected)
	return suffix != "" && !strings.Contains(suffix, "/") && (session == "" || suffix == session)
}

func cloudConfig(ctx context.Context, in Inputs, profile string) (aws.Config, string, error) {
	c := in.Config()
	c.AWSProfile = profile
	a, err := identity.Load(ctx, c)
	if err != nil {
		return a, "", fail("credentials_unavailable", "cannot load the selected AWS profile; authenticate its source and retry")
	}
	request, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := sts.NewFromConfig(a).GetCallerIdentity(request, &sts.GetCallerIdentityInput{})
	if err != nil || out == nil {
		if request.Err() != nil {
			return a, "", request.Err()
		}
		return a, "", fail("identity_unavailable", "cannot verify the selected AWS identity; refresh authentication and retry")
	}
	if aws.ToString(out.Account) != in.Account {
		return a, "", fail("account_mismatch", "AWS identity differs from the confirmed expected account")
	}
	arn := aws.ToString(out.Arn)
	if in.Principal != "" && profile == in.SetupProfile && !samePrincipal(arn, in.Principal, "") {
		return a, "", fail("principal_mismatch", "setup credentials resolved to a different principal")
	}
	return a, arn, nil
}

func (AWSCloud) Caller(ctx context.Context, in Inputs, profile string) (string, error) {
	_, arn, err := cloudConfig(ctx, in, profile)
	return arn, err
}

func (AWSCloud) ResolvePrincipal(ctx context.Context, in Inputs, caller string) (string, error) {
	a, _, err := cloudConfig(ctx, in, in.SetupProfile)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(caller, "arn:aws:iam::"+in.Account+":user/") {
		return caller, nil
	}
	prefix := "arn:aws:sts::" + in.Account + ":assumed-role/"
	if !strings.HasPrefix(caller, prefix) {
		return "", fail("principal_invalid", "choose an existing IAM user or role; account root and other principal kinds are unsupported")
	}
	parts := strings.Split(strings.TrimPrefix(caller, prefix), "/")
	if len(parts) != 2 {
		return "", invalid("cannot resolve the source IAM role")
	}
	request, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := iam.NewFromConfig(a).GetRole(request, &iam.GetRoleInput{RoleName: aws.String(parts[0])})
	if err != nil || out == nil || out.Role == nil || !samePrincipal(caller, aws.ToString(out.Role.Arn), "") {
		return "", fail("principal_unavailable", "cannot resolve the source IAM role and its path; grant setup GetRole for the source role and retry")
	}
	return aws.ToString(out.Role.Arn), nil
}

func (AWSCloud) Image(ctx context.Context, in Inputs) (string, error) {
	a, _, err := cloudConfig(ctx, in, in.SetupProfile)
	if err != nil {
		return "", err
	}
	request, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := in.AMI
	if id == "" {
		out, err := ssm.NewFromConfig(a).GetParameter(request, &ssm.GetParameterInput{Name: aws.String("/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id")})
		if err != nil || out == nil || out.Parameter == nil {
			return "", fail("image_unavailable", "cannot resolve the Canonical Ubuntu image")
		}
		id = aws.ToString(out.Parameter.Value)
	}
	out, err := ec2.NewFromConfig(a).DescribeImages(request, &ec2.DescribeImagesInput{ImageIds: []string{id}, Owners: []string{"099720109477"}})
	if err != nil || out == nil || len(out.Images) != 1 {
		return "", fail("image_unavailable", "cannot verify the selected Canonical image")
	}
	image := out.Images[0]
	name := regexp.MustCompile(`^ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24\.04-amd64-server-[0-9]+$`)
	if aws.ToString(image.ImageId) != id || aws.ToString(image.OwnerId) != "099720109477" || image.State != "available" || image.Architecture != "x86_64" || image.VirtualizationType != "hvm" || aws.ToString(image.RootDeviceName) != "/dev/sda1" || !name.MatchString(aws.ToString(image.Name)) {
		return "", fail("image_invalid", "image does not meet the pinned Ubuntu 24.04 x86-64 provenance requirements")
	}
	root := false
	for _, b := range image.BlockDeviceMappings {
		if aws.ToString(b.DeviceName) == "/dev/sda1" && b.Ebs != nil && aws.ToInt32(b.Ebs.VolumeSize) > 0 && aws.ToInt32(b.Ebs.VolumeSize) <= 100 {
			root = true
		}
	}
	if !root {
		return "", fail("image_invalid", "selected image has no verified root snapshot fitting the 100 GiB disk")
	}
	return id, nil
}

func missing(err error, codes ...string) bool {
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

func (AWSCloud) SpotRole(ctx context.Context, in Inputs, create bool) (bool, error) {
	a, _, err := cloudConfig(ctx, in, in.SetupProfile)
	if err != nil {
		return false, err
	}
	request, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := iam.NewFromConfig(a)
	out, err := api.GetRole(request, &iam.GetRoleInput{RoleName: aws.String("AWSServiceRoleForEC2Spot")})
	want := "arn:aws:iam::" + in.Account + ":role/aws-service-role/spot.amazonaws.com/AWSServiceRoleForEC2Spot"
	if err == nil && out != nil && out.Role != nil && aws.ToString(out.Role.Arn) == want {
		return true, nil
	}
	if !missing(err, "NoSuchEntity") {
		return false, fail("spot_role_unavailable", "cannot verify the account Spot service-linked role; a denied read is not absence")
	}
	if !create {
		return false, nil
	}
	created, err := api.CreateServiceLinkedRole(request, &iam.CreateServiceLinkedRoleInput{AWSServiceName: aws.String("spot.amazonaws.com")}, func(o *iam.Options) { o.Retryer = aws.NopRetryer{} })
	if err != nil || created == nil || created.Role == nil || aws.ToString(created.Role.Arn) != want {
		return false, fail("spot_role_uncertain", "Spot role creation did not return verified completion; resume to observe it before retrying")
	}
	return true, nil
}

// Bucket returns absent only on a typed not-found response. Existing bucket
// scope/protections and exact key existence are verified with expected owner.
func (AWSCloud) Bucket(ctx context.Context, in Inputs, key string) (string, error) {
	a, _, err := cloudConfig(ctx, in, in.SetupProfile)
	if err != nil {
		return "", err
	}
	request, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	api := s3.NewFromConfig(a)
	owner := aws.String(in.Account)
	bucket := aws.String(in.Bucket)
	_, err = api.HeadBucket(request, &s3.HeadBucketInput{Bucket: bucket, ExpectedBucketOwner: owner})
	if missing(err, "NotFound", "NoSuchBucket") && key == "" {
		return "absent", nil
	}
	if err != nil {
		return "", fail("backend_unavailable", "cannot verify state bucket ownership; an inaccessible bucket is not an unused name")
	}
	if key == "" {
		return "exists", nil
	}
	loc, err := api.GetBucketLocation(request, &s3.GetBucketLocationInput{Bucket: bucket, ExpectedBucketOwner: owner})
	if err != nil || loc == nil || string(loc.LocationConstraint) != in.Region {
		return "", recoveryError()
	}
	version, err := api.GetBucketVersioning(request, &s3.GetBucketVersioningInput{Bucket: bucket, ExpectedBucketOwner: owner})
	if err != nil || version == nil || version.Status != "Enabled" {
		return "", recoveryError()
	}
	block, err := api.GetPublicAccessBlock(request, &s3.GetPublicAccessBlockInput{Bucket: bucket, ExpectedBucketOwner: owner})
	if err != nil || block == nil || block.PublicAccessBlockConfiguration == nil {
		return "", recoveryError()
	}
	p := block.PublicAccessBlockConfiguration
	if !aws.ToBool(p.BlockPublicAcls) || !aws.ToBool(p.BlockPublicPolicy) || !aws.ToBool(p.IgnorePublicAcls) || !aws.ToBool(p.RestrictPublicBuckets) {
		return "", recoveryError()
	}
	enc, err := api.GetBucketEncryption(request, &s3.GetBucketEncryptionInput{Bucket: bucket, ExpectedBucketOwner: owner})
	if err != nil || enc == nil || enc.ServerSideEncryptionConfiguration == nil || len(enc.ServerSideEncryptionConfiguration.Rules) != 1 {
		return "", recoveryError()
	}
	rule := enc.ServerSideEncryptionConfiguration.Rules[0]
	if rule.ApplyServerSideEncryptionByDefault == nil || rule.ApplyServerSideEncryptionByDefault.SSEAlgorithm != "AES256" {
		return "", recoveryError()
	}
	_, err = api.HeadObject(request, &s3.HeadObjectInput{Bucket: bucket, Key: aws.String(key), ExpectedBucketOwner: owner})
	if missing(err, "NotFound", "NoSuchKey") {
		return "empty", nil
	}
	if err != nil {
		return "", recoveryError()
	}
	return "present", nil
}

func (AWSCloud) Verify(ctx context.Context, in Inputs, m config.Manifest, configurationOnly bool, after time.Time) []foundation.Check {
	a, arn, err := cloudConfig(ctx, in, in.OperatorProfile)
	role, ok := m.Roles["operator"]
	if err != nil || !ok || !samePrincipal(arn, role.ARN, "devbox-"+in.Deployment+"-"+in.Owner) {
		return []foundation.Check{{Name: "operator_identity", Err: fail("operator_identity", "verification must use the exact manifest operator role and session name")}}
	}
	c, err := config.Load(in.ConfigPath, config.Overrides{AWSProfile: in.OperatorProfile})
	if err != nil {
		return []foundation.Check{{Name: "configuration", Err: err}}
	}
	p, err := config.LoadProfile(c.ProfileFile)
	if err != nil {
		return []foundation.Check{{Name: "profile", Err: err}}
	}
	return foundation.VerifySetup(ctx, a, m, p, configurationOnly, after)
}
