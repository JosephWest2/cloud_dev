package foundation

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (f *fake) GetBucketLocation(_ context.Context, input *s3.GetBucketLocationInput, _ ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error) {
	if f.nilResponse == "GetBucketLocation" {
		return nil, nil
	}
	out := &s3.GetBucketLocationOutput{}
	err := f.respond("GetBucketLocation", input, out)
	return out, err
}

func (f *fake) GetBucketTagging(_ context.Context, input *s3.GetBucketTaggingInput, _ ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error) {
	if f.nilResponse == "GetBucketTagging" {
		return nil, nil
	}
	out := &s3.GetBucketTaggingOutput{}
	err := f.respond("GetBucketTagging", input, out)
	return out, err
}

func (f *fake) GetBucketPolicy(_ context.Context, input *s3.GetBucketPolicyInput, _ ...func(*s3.Options)) (*s3.GetBucketPolicyOutput, error) {
	if f.nilResponse == "GetBucketPolicy" {
		return nil, nil
	}
	out := &s3.GetBucketPolicyOutput{}
	err := f.respond("GetBucketPolicy", input, out)
	return out, err
}

func (f *fake) GetBucketEncryption(_ context.Context, input *s3.GetBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error) {
	if f.nilResponse == "GetBucketEncryption" {
		return nil, nil
	}
	out := &s3.GetBucketEncryptionOutput{}
	err := f.respond("GetBucketEncryption", input, out)
	return out, err
}

func (f *fake) GetBucketOwnershipControls(_ context.Context, input *s3.GetBucketOwnershipControlsInput, _ ...func(*s3.Options)) (*s3.GetBucketOwnershipControlsOutput, error) {
	if f.nilResponse == "GetBucketOwnershipControls" {
		return nil, nil
	}
	out := &s3.GetBucketOwnershipControlsOutput{}
	err := f.respond("GetBucketOwnershipControls", input, out)
	return out, err
}

func (f *fake) GetPublicAccessBlock(_ context.Context, input *s3.GetPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error) {
	if f.nilResponse == "GetPublicAccessBlock" {
		return nil, nil
	}
	out := &s3.GetPublicAccessBlockOutput{}
	err := f.respond("GetPublicAccessBlock", input, out)
	return out, err
}

func (f *fake) GetBucketVersioning(_ context.Context, input *s3.GetBucketVersioningInput, _ ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	if f.nilResponse == "GetBucketVersioning" {
		return nil, nil
	}
	out := &s3.GetBucketVersioningOutput{}
	err := f.respond("GetBucketVersioning", input, out)
	return out, err
}

func (f *fake) GetBucketLifecycleConfiguration(_ context.Context, input *s3.GetBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	if f.nilResponse == "GetBucketLifecycleConfiguration" {
		return nil, nil
	}
	out := &s3.GetBucketLifecycleConfigurationOutput{}
	err := f.respond("GetBucketLifecycleConfiguration", input, out)
	return out, err
}

func (f *fake) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.nilResponse == "HeadObject" {
		return nil, nil
	}
	out := &s3.HeadObjectOutput{}
	err := f.respond("HeadObject", input, out)
	return out, err
}
