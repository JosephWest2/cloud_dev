package identity

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

type fakeSTS struct {
	account string
	err     error
}

func (f fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: aws.String(f.account)}, f.err
}

func TestIdentityFailuresAreActionableAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name, account string
		err           error
		code          string
	}{
		{"correct", "123456789012", nil, ""},
		{"wrong", "000000000000", nil, "account_mismatch"},
		{"empty", "", nil, "account_mismatch"},
		{"expired", "", &smithy.GenericAPIError{Code: "ExpiredToken", Message: "SECRET"}, "credentials_expired"},
		{"invalid", "", &smithy.GenericAPIError{Code: "InvalidClientTokenId", Message: "SECRET"}, "credentials_invalid"},
		{"provider", "", errors.New("SECRET"), "identity_unavailable"},
		{"unknown API code", "", &smithy.GenericAPIError{Code: "SECRET", Message: "SECRET"}, "identity_unavailable"},
		{"timeout", "", context.DeadlineExceeded, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(context.Background(), fakeSTS{tc.account, tc.err}, "123456789012")
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var f *Failure
			if !errors.As(err, &f) || f.Code != tc.code || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unsafe/wrong error: %v", err)
			}
		})
	}
}

func TestSDKCredentialSelection(t *testing.T) {
	testutil.IsolateAWS(t)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", testutil.Write(t, filepath.Join(t.TempDir(), "credentials"), "[selected]\naws_access_key_id=PROFILE_KEY\naws_secret_access_key=PROFILE_SECRET\n"))
	t.Setenv("AWS_ACCESS_KEY_ID", "ENV_KEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ENV_SECRET")
	for _, tc := range []struct{ profile, want string }{{"selected", "PROFILE_KEY"}, {"", "ENV_KEY"}} {
		a, err := Load(context.Background(), config.Config{Region: "us-east-2", AWSProfile: tc.profile})
		if err != nil {
			t.Fatal(err)
		}
		creds, err := a.Credentials.Retrieve(context.Background())
		if err != nil || creds.AccessKeyID != tc.want {
			t.Fatal("SDK did not honor profile/chain selection")
		}
		if a.Region != "us-east-2" {
			t.Fatal("explicit region lost")
		}
	}
	_, err := Load(context.Background(), config.Config{Region: "us-east-2", AWSProfile: "missing"})
	if err == nil {
		t.Fatal("missing explicit profile must not fall back to environment credentials")
	}
}
