package foundation

import (
	"context"
	"errors"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// CheckResults is read-only and independent of live instances and SSH resources.
// This checks configured safeguards, not effective authorization for every object.
func CheckResults(ctx context.Context, api S3, r config.Results, deployment, owner string) error {
	fail := errors.New("result storage is missing, inaccessible or differs from the trusted scope, policy or retention")
	if api == nil {
		return fail
	}
	bucket, account := aws.String(r.Bucket), aws.String(r.ExpectedBucketOwner)
	location, err := api.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: bucket, ExpectedBucketOwner: account})
	if err != nil || location == nil || string(location.LocationConstraint) != r.Region {
		return fail
	}
	tags, err := api.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket, ExpectedBucketOwner: account})
	if err != nil || tags == nil {
		return fail
	}
	gotTags := map[string]string{}
	for _, tag := range tags.TagSet {
		gotTags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	if gotTags["ManagedBy"] != "devbox" || gotTags["Deployment"] != deployment || gotTags["Owner"] != owner {
		return fail
	}
	policy, err := api.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: bucket, ExpectedBucketOwner: account})
	if err != nil || policy == nil {
		return fail
	}
	hash, err := jsonDigest(aws.ToString(policy.Policy))
	if err != nil || hash != r.PolicySHA256 {
		return fail
	}
	block, err := api.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: bucket, ExpectedBucketOwner: account})
	if err != nil || block == nil || block.PublicAccessBlockConfiguration == nil {
		return fail
	}
	b := block.PublicAccessBlockConfiguration
	if !aws.ToBool(b.BlockPublicAcls) || !aws.ToBool(b.BlockPublicPolicy) || !aws.ToBool(b.IgnorePublicAcls) || !aws.ToBool(b.RestrictPublicBuckets) {
		return fail
	}
	ownership, err := api.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: bucket, ExpectedBucketOwner: account})
	if err != nil || ownership == nil || ownership.OwnershipControls == nil || len(ownership.OwnershipControls.Rules) != 1 || ownership.OwnershipControls.Rules[0].ObjectOwnership != s3types.ObjectOwnershipBucketOwnerEnforced {
		return fail
	}
	encryption, err := api.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: bucket, ExpectedBucketOwner: account})
	if err != nil || encryption == nil || encryption.ServerSideEncryptionConfiguration == nil || len(encryption.ServerSideEncryptionConfiguration.Rules) != 1 {
		return fail
	}
	e := encryption.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault
	if e == nil || e.SSEAlgorithm != s3types.ServerSideEncryptionAes256 || e.KMSMasterKeyID != nil {
		return fail
	}
	versioning, err := api.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: bucket, ExpectedBucketOwner: account})
	// Suspended is not a never-versioned bucket: hidden noncurrent versions may remain.
	if err != nil || versioning == nil || versioning.Status != "" {
		return fail
	}
	lifecycle, err := api.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket, ExpectedBucketOwner: account})
	if err != nil || lifecycle == nil || !validResultLifecycle(lifecycle.Rules, r) {
		return fail
	}
	return nil
}

func validResultLifecycle(rules []s3types.LifecycleRule, r config.Results) bool {
	// The exact supported policy has one expiration and one multipart-abort action,
	// both scoped to the result prefix. Extra rules could shorten the promise.
	expirations, aborts := 0, 0
	for _, rule := range rules {
		if rule.Status != s3types.ExpirationStatusEnabled || rule.Filter == nil || aws.ToString(rule.Filter.Prefix) != r.Prefix || rule.Filter.And != nil || rule.Filter.Tag != nil || rule.Filter.ObjectSizeGreaterThan != nil || rule.Filter.ObjectSizeLessThan != nil || rule.Prefix != nil || len(rule.Transitions) != 0 || len(rule.NoncurrentVersionTransitions) != 0 || rule.NoncurrentVersionExpiration != nil {
			return false
		}
		if rule.Expiration != nil {
			expirations++
			x := rule.Expiration
			if x.Days == nil || int(*x.Days) < r.RetentionDays || *x.Days > 365 || x.Date != nil || aws.ToBool(x.ExpiredObjectDeleteMarker) {
				return false
			}
		}
		if rule.AbortIncompleteMultipartUpload != nil {
			aborts++
			if aws.ToInt32(rule.AbortIncompleteMultipartUpload.DaysAfterInitiation) != 1 {
				return false
			}
		}
		if rule.Expiration == nil && rule.AbortIncompleteMultipartUpload == nil {
			return false
		}
	}
	return expirations == 1 && aborts == 1
}

func checkExecution(ctx context.Context, api SSM, store S3, m config.Manifest) error {
	fail := errors.New("execution document or runner artifact is missing, inaccessible or changed")
	if api == nil || store == nil {
		return fail
	}
	e := m.Execution
	out, err := api.GetDocument(ctx, &ssm.GetDocumentInput{Name: aws.String(e.Name), DocumentVersion: aws.String(e.Version), DocumentFormat: ssmtypes.DocumentFormatJson})
	if err != nil || out == nil || aws.ToString(out.Name) != e.Name || aws.ToString(out.DocumentVersion) != e.Version || out.DocumentType != ssmtypes.DocumentTypeCommand || out.Status != ssmtypes.DocumentStatusActive {
		return fail
	}
	hash, err := jsonDigest(aws.ToString(out.Content))
	if err != nil || hash != e.ContentSHA256 {
		return fail
	}
	object, err := store.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(m.Results.Bucket), ExpectedBucketOwner: aws.String(m.Results.ExpectedBucketOwner), Key: aws.String("artifacts/runner/" + e.RunnerSHA256 + "/linux-amd64")})
	if err != nil || object == nil || aws.ToInt64(object.ContentLength) <= 0 || object.Metadata["sha256"] != e.RunnerSHA256 || object.ServerSideEncryption != s3types.ServerSideEncryptionAes256 {
		return fail
	}
	// Bootstrap hashes actual artifact bytes before installing; metadata alone is
	// not an attestation that those bytes match the manifest.
	return nil
}
