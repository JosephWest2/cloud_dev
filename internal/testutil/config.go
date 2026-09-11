// Package testutil supplies non-secret setup fixtures for invariant tests.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

const Config = `schema_version = 1
expected_account = "123456789012"
region = "us-east-2"
deployment = "test"
owner = "test-owner"
aws_profile = "test"
manifest = "deployment.json"
`

const Manifest = `{
"schema_version":1,"account":"123456789012","region":"us-east-2",
"deployment":"test","owner":"test-owner","vpc_id":"vpc-12345678",
"subnet_ids":["subnet-12345678"],"security_group_id":"sg-12345678",
"instance_profile_arn":"arn:aws:iam::123456789012:instance-profile/devbox",
"images":{"agent":{"ami_id":"ami-12345678","architecture":"x86_64",
"launch_template_id":"lt-12345678","launch_template_version":"1"}}}
`

func Write(t *testing.T, path, data string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func Setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	Write(t, filepath.Join(dir, "deployment.json"), Manifest)
	return Write(t, filepath.Join(dir, "config.toml"), Config)
}

func IsolateAWS(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_STS", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", Write(t, filepath.Join(t.TempDir(), "config"), ""))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", Write(t, filepath.Join(t.TempDir(), "credentials"), ""))
}
