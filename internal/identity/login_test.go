package identity

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

func loginConfig(t *testing.T, body, credentials string) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string]string{"config": body, "credentials": credentials} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
}

func TestLoginSource(t *testing.T) {
	for _, tc := range []struct {
		name, body, credentials, want string
		sso                           bool
	}{
		{"browser", "[profile selected]\nlogin_session=test\n", "", "selected", false},
		{"legacy sso", "[profile selected]\nsso_start_url=https://example.com/start\nsso_region=us-east-2\nsso_account_id=123456789012\nsso_role_name=Test\n", "", "selected", true},
		{"sso session", "[profile selected]\nsso_session=test\nsso_account_id=123456789012\nsso_role_name=Test\n[sso-session test]\nsso_start_url=https://example.com/start\nsso_region=us-east-2\n", "", "selected", true},
		{"operator bridge", "[profile selected]\nrole_arn=arn:aws:iam::123456789012:role/operator\nsource_profile=bridge\n[profile bridge]\ncredential_process=aws configure export-credentials --profile source --format process\n[profile source]\nlogin_session=test\n", "", "source", false},
		{"quoted bridge", "[profile selected]\ncredential_process=\"/a path/aws\" configure export-credentials --profile source --format process\n[profile source]\nlogin_session=test\n", "", "source", false},
		{"arbitrary helper", "[profile selected]\ncredential_process=helper --profile source\n[profile source]\nlogin_session=test\n", "", "", false},
		{"shell injection", "[profile selected]\ncredential_process=aws configure export-credentials --profile source --format process; evil\n[profile source]\nlogin_session=test\n", "", "", false},
		{"cycle", "[profile selected]\ncredential_process=aws configure export-credentials --profile selected --format process\n", "", "", false},
		{"static shadow", "[profile selected]\nlogin_session=test\n", "[selected]\naws_access_key_id=key\naws_secret_access_key=secret\n", "", false},
		{"missing", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loginConfig(t, tc.body, tc.credentials)
			got, sso := loginSource(context.Background(), "selected")
			if got != tc.want || sso != tc.sso {
				t.Fatalf("got %q %v; want %q %v", got, sso, tc.want, tc.sso)
			}
		})
	}
}

func TestPrepareLogin(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic    string
		probeOK, loginFails bool
		calls               int
	}{
		{name: "valid", probeOK: true, calls: 1},
		{name: "expired", diagnostic: "Your session has expired. SECRET", calls: 2},
		{name: "missing session", diagnostic: "Error loading login session token: SECRET", calls: 2},
		{name: "network", diagnostic: "Could not connect to endpoint SECRET", calls: 1},
		{name: "denied", diagnostic: "AccessDenied SECRET", calls: 1},
		{name: "failed login", diagnostic: "Your session has expired. SECRET", loginFails: true, calls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loginConfig(t, "[profile selected]\nlogin_session=test\n", "")
			calls := 0
			var output strings.Builder
			err := prepareLogin(context.Background(), config.Config{AWSProfile: "selected"}, strings.NewReader(""), &output, func(ctx context.Context, args []string, in io.Reader, out io.Writer) (string, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Minute {
					t.Fatal("unbounded authentication")
				}
				if calls == 1 {
					if out != nil || in != nil {
						t.Fatal("credential probe attached to terminal")
					}
					if tc.probeOK {
						return "", nil
					}
					return tc.diagnostic, errors.New("SECRET")
				}
				if !reflect.DeepEqual(args, []string{"login", "--profile", "selected"}) || out == nil || in == nil {
					t.Fatalf("incorrect login: %v", args)
				}
				if tc.loginFails {
					return "", errors.New("SECRET")
				}
				return "", nil
			})
			if calls != tc.calls || (err != nil) != tc.loginFails {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if strings.Contains(output.String(), "SECRET") || (err != nil && strings.Contains(err.Error(), "SECRET")) {
				t.Fatal("diagnostic leak")
			}
		})
	}
}

func TestLoginFailureStopsAWSLoad(t *testing.T) {
	failure := &Failure{"authentication_failed", "login failed"}
	ctx := context.WithValue(context.Background(), loginFailureKey{}, failure)
	_, err := Load(ctx, config.Config{})
	if err != failure {
		t.Fatalf("got %v", err)
	}
}

func TestLoginProbeDiscardsCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte("#!/bin/sh\necho SECRET_CREDENTIALS\necho expired >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	diagnostic, err := runLoginProcess(context.Background(), []string{"configure", "export-credentials"}, nil, nil)
	if err == nil || strings.Contains(diagnostic, "SECRET") || !strings.Contains(diagnostic, "expired") {
		t.Fatalf("unexpected probe result %q %v", diagnostic, err)
	}
}

func TestLoginCancellationAndSSO(t *testing.T) {
	loginConfig(t, "[profile selected]\nsso_start_url=https://example.com/start\nsso_region=us-east-2\nsso_account_id=123456789012\nsso_role_name=Test\n", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := prepareLogin(ctx, config.Config{AWSProfile: "selected"}, strings.NewReader(""), io.Discard, func(ctx context.Context, args []string, _ io.Reader, _ io.Writer) (string, error) {
		calls++
		if calls == 1 {
			return "Error loading SSO Token: token does not exist", errors.New("missing")
		}
		if !reflect.DeepEqual(args, []string{"sso", "login", "--profile", "selected"}) {
			t.Fatalf("unexpected args %v", args)
		}
		cancel()
		return "", ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
