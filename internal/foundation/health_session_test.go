package foundation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

type healthSTSFunc func(context.Context, *sts.AssumeRoleInput) (*sts.AssumeRoleOutput, error)

func (f healthSTSFunc) AssumeRole(ctx context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	return f(ctx, in)
}
func TestHealthCredentialsBoundedAndIndependent(t *testing.T) {
	f, m, _, e, _ := evidenceFixture(t)
	original := f.hook
	f.hook = func(method string, in any) (string, bool) {
		doc, ok := original(method, in)
		if method == "GetRolePolicy" || method == "ListRolePolicies" {
			doc = strings.ReplaceAll(doc, `"PolicyName":"`+roleName(e.HealthRole.ARN)+`"`, `"PolicyName":"`+e.HealthRole.PolicyName+`"`)
			doc = strings.ReplaceAll(doc, `"PolicyNames":["`+roleName(e.HealthRole.ARN)+`"]`, `"PolicyNames":["`+e.HealthRole.PolicyName+`"]`)
		}
		return doc, ok
	}
	operator := aws.Config{Region: m.Region, Credentials: credentials.NewStaticCredentialsProvider("operator", "operator-secret", "operator-token")}
	calls := 0
	api := healthSTSFunc(func(ctx context.Context, in *sts.AssumeRoleInput) (*sts.AssumeRoleOutput, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("unbounded health assumption")
		}
		if aws.ToString(in.RoleArn) != e.HealthRole.ARN || aws.ToInt32(in.DurationSeconds) != 900 || aws.ToString(in.RoleSessionName) != "devbox-cleanup-health" {
			t.Error("unexpected authority", in)
		}
		return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{AccessKeyId: aws.String("health"), SecretAccessKey: aws.String("health-secret"), SessionToken: aws.String("health-token"), Expiration: aws.Time(time.Now().Add(15 * time.Minute))}}, nil
	})
	health, err := healthConfig(context.Background(), api, f, operator, m, e)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := health.Credentials.Retrieve(context.Background())
	still, _ := operator.Credentials.Retrieve(context.Background())
	if got.AccessKeyID != "health" || still.AccessKeyID != "operator" || calls != 1 {
		t.Fatal("health authority escaped its local config")
	}
	f.fail = "GetRolePolicy"
	if _, err := healthConfig(context.Background(), api, f, operator, m, e); err == nil || calls != 1 {
		t.Fatal("unverified health role was assumed")
	}
	f.fail = ""
	denied := healthSTSFunc(func(context.Context, *sts.AssumeRoleInput) (*sts.AssumeRoleOutput, error) {
		return nil, errors.New("opaque-secret")
	})
	if _, err := healthConfig(context.Background(), denied, f, operator, m, e); err == nil || strings.Contains(err.Error(), "opaque") {
		t.Fatal("unsafe role failure", err)
	}
}
