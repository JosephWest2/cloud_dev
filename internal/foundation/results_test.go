package foundation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestResultStorageControlsAndOwnerBoundRequests(t *testing.T) {
	f, m, _ := fixture(t)
	if err := CheckResults(context.Background(), f, m.Results, m.Deployment, m.Owner); err != nil {
		t.Fatal(err)
	}
	if err := checkExecution(context.Background(), f, f, m); err != nil {
		t.Fatal(err)
	}
	for name, input := range f.inputs {
		if name == "GetDocument" {
			continue
		}
		b, _ := json.Marshal(input)
		var fields map[string]any
		_ = json.Unmarshal(b, &fields)
		if fields["Bucket"] != m.Results.Bucket || fields["ExpectedBucketOwner"] != m.Account {
			t.Fatalf("unbound %s request", name)
		}
	}
	if key := aws.ToString(f.inputs["HeadObject"].(*s3.HeadObjectInput).Key); key != "artifacts/runner/"+m.Execution.RunnerSHA256+"/linux-amd64" {
		t.Fatal("unpinned artifact key", key)
	}
}

func TestResultStorageAndRunnerDrift(t *testing.T) {
	for _, tc := range []struct{ name, api, old, new string }{
		{"wrong region", "GetBucketLocation", "us-east-2", "us-west-2"},
		{"wrong owner tag", "GetBucketTagging", "test-owner", "other-owner"},
		{"changed policy", "GetBucketPolicy", "UpdateInstanceInformation", "SendCommand"},
		{"public access enabled", "GetPublicAccessBlock", `"BlockPublicPolicy":true`, `"BlockPublicPolicy":false`},
		{"ACLs enabled", "GetBucketOwnershipControls", "BucketOwnerEnforced", "ObjectWriter"},
		{"different encryption", "GetBucketEncryption", "AES256", "aws:kms"},
		{"hidden noncurrent versions", "GetBucketVersioning", `{}`, `{"Status":"Suspended"}`},
		{"short retention", "GetBucketLifecycleConfiguration", `"Days":30`, `"Days":29`},
		{"wrong prefix", "GetBucketLifecycleConfiguration", "test-owner", "other-owner"},
		{"disabled retention", "GetBucketLifecycleConfiguration", `"Status":"Enabled"`, `"Status":"Disabled"`},
		{"extra early expiry", "GetBucketLifecycleConfiguration", `"Rules":[`, `"Rules":[{"Status":"Enabled","Filter":{"Prefix":""},"Expiration":{"Days":1}},`},
		{"artifact empty", "HeadObject", `"ContentLength":1024`, `"ContentLength":0`},
		{"artifact changed", "HeadObject", strings.Repeat("a", 64), strings.Repeat("b", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, _ := fixture(t)
			before := f.responses[tc.api]
			f.responses[tc.api] = strings.Replace(before, tc.old, tc.new, 1)
			if f.responses[tc.api] == before {
				t.Fatal("fixture did not change")
			}
			err := CheckResults(context.Background(), f, m.Results, m.Deployment, m.Owner)
			if tc.api == "HeadObject" {
				err = checkExecution(context.Background(), f, f, m)
			}
			if err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}
