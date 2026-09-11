// Package identity verifies the selected AWS identity before resource operations.
package identity

import (
	"context"
	"errors"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/logging"
)

type STS interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// Failure contains only allowlisted diagnostics, never an SDK error or provider output.
type Failure struct{ Code, Message string }

func (f *Failure) Error() string { return f.Message }

func safeError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &Failure{"timeout", "AWS identity check timed out or was canceled; check connectivity and retry with --timeout 60s"}
	}
	var source awsconfig.SharedConfigAssumeRoleError
	if errors.As(err, &source) {
		return &Failure{"role_source_unavailable", "cannot load the assume-role profile's credential source; check source_profile; for an aws login source use the credential_process bridge in docs/setup.md"}
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "ExpiredToken", "ExpiredTokenException", "RequestExpired":
			return &Failure{"credentials_expired", "AWS credentials expired; refresh the selected profile (for SSO: aws sso login --profile PROFILE) and retry"}
		case "InvalidClientTokenId", "UnrecognizedClientException", "SignatureDoesNotMatch":
			return &Failure{"credentials_invalid", "AWS rejected the credentials; refresh the selected credential source and check the local clock"}
		case "AccessDenied", "AccessDeniedException":
			return &Failure{"identity_denied", "AWS identity request was denied; check the selected profile and applicable AWS policies"}
		}
	}
	return &Failure{"identity_unavailable", "cannot verify AWS identity; check the selected profile, refresh its credentials (for SSO: aws sso login --profile PROFILE), and check network connectivity"}
}

func Load(ctx context.Context, c config.Config) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(c.Region),
		awsconfig.WithLogger(logging.Nop{}),
		awsconfig.WithClientLogMode(0),
		awsconfig.WithRetryMaxAttempts(2),
	}
	if c.AWSProfile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.AWSProfile))
	}
	a, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, safeError(err)
	}
	return a, nil
}

func Verify(ctx context.Context, client STS, expectedAccount string) error {
	out, err := client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return safeError(err)
	}
	if out == nil || aws.ToString(out.Account) != expectedAccount {
		return &Failure{"account_mismatch", "AWS identity does not match expected_account; select the intended AWS profile or correct the configuration before continuing"}
	}
	return nil
}

func Check(ctx context.Context, c config.Config) error {
	a, err := Load(ctx, c)
	if err != nil {
		return err
	}
	return Verify(ctx, sts.NewFromConfig(a), c.ExpectedAccount)
}
