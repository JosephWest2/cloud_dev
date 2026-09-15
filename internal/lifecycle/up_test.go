package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func setupUp(t *testing.T) (*Service, config.Manifest, config.Profile, Store, *fakeEC2) {
	t.Helper()
	path := testutil.Setup(t)
	t.Setenv("AWS_PROFILE", "")
	c, err := config.Load(path, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	m, err := config.LoadManifest(c.Manifest, c, p)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeEC2{}
	s := testService(api)
	s.Scope = c
	s.VerifyFoundation = func(context.Context, config.Manifest, config.Profile) error { return nil }
	store := Store{Dir: filepath.Join(t.TempDir(), "requests")}
	if err := os.MkdirAll(store.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	return s, m, p, store, api
}
func quiet(Receipt, string) error { return nil }
func launched(in *ec2.RunInstancesInput) types.Instance {
	i := worker("i-12345678")
	i.ClientToken = in.ClientToken
	i.ImageId = in.ImageId
	i.InstanceType = in.InstanceType
	i.Tags = append([]types.Tag{}, in.TagSpecifications[0].Tags...)
	i.Tags = append(i.Tags, types.Tag{Key: aws.String("aws:ec2launchtemplate:id"), Value: in.LaunchTemplate.LaunchTemplateId}, types.Tag{Key: aws.String("aws:ec2launchtemplate:version"), Value: in.LaunchTemplate.Version})
	return i
}
func TestLegacyLostLaunchResponseAndRestartReplay(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
	r.State = "dispatched"
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	original := launchInput(r)
	instance := launched(original)
	invisible := 0
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		if invisible < 2 {
			invisible++
			return inventory(), nil
		}
		return inventory(instance), nil
	}
	for range 2 {
		out, err := s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
		if err != nil || out.Status != "allocated" || api.launches != 0 {
			t.Fatalf("%+v %v launches=%d", out, err, api.launches)
		}
	}
	saved, err := store.Load(r.RequestID)
	if err != nil || !reflect.DeepEqual(launchInput(saved), original) {
		t.Fatal("historical launch serialization changed", err)
	}
}

func TestLegacyUncertainLaunchNeverReallocates(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
	r.State = "dispatched"
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }
	for range 2 {
		out, err := s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
		if err == nil || out.Status != "outcome_unresolved" || api.launches != 0 {
			t.Fatalf("%+v %v", out, err)
		}
	}
}

func TestPreparedReceiptReplayAndParameterChanges(t *testing.T) {
	for _, variant := range []string{"same", "image", "type", "disk", "template", "scope"} {
		t.Run(variant, func(t *testing.T) {
			s, m, p, store, api := setupUp(t)
			r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
			unlock, err := store.Lock(context.Background(), r.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Save(r); err != nil {
				t.Fatal(err)
			}
			unlock()
			var instance *types.Instance
			api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				if instance == nil {
					return inventory(), nil
				}
				return inventory(*instance), nil
			}
			api.run = func(in *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
				if aws.ToString(in.ClientToken) != r.ClientToken {
					t.Fatal("token changed")
				}
				i := launched(in)
				instance = &i
				return &ec2.RunInstancesOutput{Instances: []types.Instance{i}}, nil
			}
			switch variant {
			case "image":
				img := m.Images["agent"]
				img.AMIID = "ami-87654321"
				m.Images["agent"] = img
			case "type":
				p.InstanceTypes = []string{"c7a.2xlarge"}
			case "disk":
				p.DiskGB++
			case "template":
				img := m.Images["agent"]
				img.LaunchTemplateVersion = "2"
				m.Images["agent"] = img
			case "scope":
				s.Scope.Region = "us-west-2"
			}
			_, err = s.Up(context.Background(), m, p, UpOptions{Resume: r.RequestID}, store, quiet)
			var f *Failure
			if !errors.As(err, &f) || api.launches != 0 {
				t.Fatalf("legacy request allocated: %v", err)
			}
			if variant != "scope" && f.Code != "legacy_request_no_expiry" {
				t.Fatal(err)
			}
		})
	}
}
func TestDispatchCrashGapRemainsUnresolved(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
	r.State = "dispatched"
	unlock, _ := store.Lock(context.Background(), r.RequestID)
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	unlock()
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }
	result, err := s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
	if err == nil || result.Status != "outcome_unresolved" || api.launches != 0 {
		t.Fatalf("crash gap relaunched: %+v %v", result, err)
	}
}
func TestConcurrentPreparedResume(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
	unlock, _ := store.Lock(context.Background(), r.RequestID)
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	unlock()
	var instance *types.Instance
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		if instance == nil {
			return inventory(), nil
		}
		return inventory(*instance), nil
	}
	api.run = func(in *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
		i := launched(in)
		instance = &i
		return &ec2.RunInstancesOutput{Instances: []types.Instance{i}}, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Up(context.Background(), m, p, UpOptions{Resume: r.RequestID}, store, quiet)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		var f *Failure
		if !errors.As(err, &f) || f.Code != "legacy_request_no_expiry" {
			t.Fatal(err)
		}
	}
	if api.launches != 0 {
		t.Fatal("legacy allocation", api.launches)
	}
}
func TestLegacyFreshAllocationIsRetired(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	out, err := s.Up(context.Background(), m, p, UpOptions{Name: "smoke", OnDemand: true}, store, func(Receipt, string) error { t.Fatal("announced legacy allocation"); return nil })
	var f *Failure
	if !errors.As(err, &f) || f.Code != "legacy_request_no_expiry" || api.launches != 0 || out.RequestID != "" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestLegacyObservationReceiptFailurePreservesIDs(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
	r.State = "dispatched"
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	i := launched(launchInput(r))
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		if err := os.Rename(store.Dir, store.Dir+"-moved"); err != nil {
			t.Fatal(err)
		}
		return inventory(i), nil
	}
	out, err := s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
	if err == nil || len(out.Instances) != 1 || out.Instances[0].ID != "i-12345678" || api.launches != 0 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestLockCancellationAndReceiptValidation(t *testing.T) {
	s, m, p, store, _ := setupUp(t)
	r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
	unlock, err := store.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err = store.Lock(ctx, r.RequestID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock not bounded", err)
	}
	if err = store.Save(r); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Path(r.RequestID))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("receipt permissions")
	}
	raw, _ := json.Marshal(r)
	if err = os.WriteFile(store.Path(r.RequestID), append(raw, []byte(" {}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(r.RequestID); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	if _, err = store.Load("../escape"); err == nil {
		t.Fatal("accepted path traversal")
	}
}
func TestRunCleanupWithoutManifestProfileOrReceipts(t *testing.T) {
	path := testutil.Setup(t)
	testutil.Write(t, path, testutil.Config+"profile_file='missing.toml'\ndefault_ttl='unlimited'\n")
	if err := os.Remove(filepath.Join(filepath.Dir(path), "deployment.json")); err != nil {
		t.Fatal(err)
	}
	api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }}
	deps := Dependencies{Store: &Store{Dir: "/does/not/exist"}, New: func(context.Context, config.Config) (*Service, error) { return testService(api), nil }}
	for _, command := range []string{"ls", "down"} {
		r := Run(context.Background(), path, config.Overrides{}, Options{Command: command, Target: "smoke"}, deps, io.Discard)
		if !r.OK {
			t.Fatalf("cleanup depends on launch files: %+v", r)
		}
	}
}
func TestRunInvalidConfigManifestIdentityPreventLaunch(t *testing.T) {
	for _, variant := range []string{"config", "profile", "manifest", "identity"} {
		t.Run(variant, func(t *testing.T) {
			path := testutil.Setup(t)
			switch variant {
			case "config":
				testutil.Write(t, path, "SECRET malformed")
			case "profile":
				testutil.Write(t, path, testutil.Config+"profile_file='missing.toml'\ndefault_ttl='unlimited'\n")
			case "manifest":
				testutil.Write(t, filepath.Join(filepath.Dir(path), "deployment.json"), strings.Replace(testutil.Manifest, "123456789012", "999999999999", 1))
			}
			calls := 0
			r := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", UpOptions: UpOptions{Name: "smoke", OnDemand: true}}, Dependencies{Store: &Store{Dir: t.TempDir()}, New: func(context.Context, config.Config) (*Service, error) {
				calls++
				return nil, errors.New("SECRET credentials")
			}}, io.Discard)
			if r.OK || strings.Contains(r.Message, "SECRET") || (variant != "identity" && calls != 0) {
				t.Fatalf("invalid input reached AWS %+v calls=%d", r, calls)
			}
		})
	}
}

func TestRequestAndConcurrentNameConflictsPreserveIDs(t *testing.T) {
	for _, variant := range []string{"request_duplicate", "request_token", "name_collision", "terminated"} {
		t.Run(variant, func(t *testing.T) {
			s, m, p, store, api := setupUp(t)
			r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
			r.State = "dispatched"
			unlock, _ := store.Lock(context.Background(), r.RequestID)
			if err := store.Save(r); err != nil {
				t.Fatal(err)
			}
			unlock()
			first := launched(launchInput(r))
			second := launched(launchInput(r))
			second.InstanceId = aws.String("i-87654321")
			api.describe = func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				switch variant {
				case "request_duplicate":
					return inventory(first, second), nil
				case "request_token":
					first.ClientToken = aws.String("different")
					return inventory(first), nil
				case "name_collision":
					for _, f := range in.Filters {
						if aws.ToString(f.Name) == "tag:Name" {
							return inventory(first, second), nil
						}
					}
				case "terminated":
					first.State.Name = types.InstanceStateNameTerminated
				}
				return inventory(first), nil
			}
			result, err := s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
			if api.launches != 0 || len(result.Instances) == 0 {
				t.Fatalf("missing conflicts or duplicate allocation: %+v", result)
			}
			if variant == "terminated" {
				if err != nil || result.Status != "already_terminated" {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("conflict accepted")
			}
		})
	}
}

func TestLaunchRejectionSurvivesRestartWithoutRawDiagnostics(t *testing.T) {
	for _, code := range []string{"PendingVerification", "UnauthorizedOperation", "UnexpectedSecretCode"} {
		t.Run(code, func(t *testing.T) {
			s, m, p, store, api := setupUp(t)
			api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }
			r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
			r.State = "dispatched"
			if code != "UnexpectedSecretCode" {
				r.LaunchErrorCode = code
			}
			if err := store.Save(r); err != nil {
				t.Fatal(err)
			}
			first, err := s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
			want := "outcome_unresolved"
			if code == "PendingVerification" {
				want = "launch_pending_verification"
			} else if code == "UnauthorizedOperation" {
				want = "launch_rejected"
			}
			var f *Failure
			if !errors.As(err, &f) || f.Code != want || first.Status != "outcome_unresolved" || first.RequestID == "" {
				t.Fatalf("original diagnostic: %+v %v", first, err)
			}
			resumed, err := testService(api).Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: first.RequestID}, store, quiet)
			if !errors.As(err, &f) || f.Code != want || resumed.RequestID != first.RequestID || api.launches != 0 {
				t.Fatalf("restart diagnostic or dispatch: %+v %v", resumed, err)
			}
			raw, err := os.ReadFile(store.Path(first.RequestID))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw)+f.Message, "SECRET") || strings.Contains(string(raw), "UnexpectedSecretCode") {
				t.Fatal("untrusted SDK diagnostic persisted or exposed")
			}
		})
	}
}

func TestObservedInstanceOverridesRecordedLaunchRejection(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	r, _ := newReceipt(parameters(s.Scope, m, p, "smoke"))
	r.State = "dispatched"
	r.LaunchErrorCode = "PendingVerification"
	unlock, err := store.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(r); err != nil {
		t.Fatal(err)
	}
	unlock()
	i := launched(launchInput(r))
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(i), nil }
	result, err := s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
	if err != nil || result.Status != "allocated" || api.launches != 0 {
		t.Fatalf("historical error overrode inventory: %+v %v", result, err)
	}
	saved, err := store.Load(r.RequestID)
	if err != nil || saved.State != "observed" || saved.LaunchErrorCode != "" {
		t.Fatalf("historical error not cleared: %+v %v", saved, err)
	}
}
