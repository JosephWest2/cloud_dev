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
	return s, m, p, Store{Dir: filepath.Join(t.TempDir(), "requests")}, api
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
func TestLostLaunchResponseAndRestartReplay(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	var instance *types.Instance
	invisible := 0
	api.describe = func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		if instance == nil {
			return inventory(), nil
		}
		if invisible < 2 {
			invisible++
			return inventory(), nil
		}
		return inventory(*instance), nil
	}
	var original *ec2.RunInstancesInput
	api.run = func(in *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
		original = in
		if aws.ToInt32(in.MinCount) != 1 || aws.ToInt32(in.MaxCount) != 1 || in.InstanceMarketOptions != nil || aws.ToString(in.ImageId) != m.Images["agent"].AMIID || aws.ToString(in.LaunchTemplate.Version) != "1" {
			t.Fatalf("bad launch %+v", in)
		}
		if len(in.TagSpecifications) != 2 || !reflect.DeepEqual(in.TagSpecifications[0].Tags, in.TagSpecifications[1].Tags) || len(in.TagSpecifications[0].Tags) != 7 {
			t.Fatal("missing resource tags")
		}
		r, err := store.Load(aws.ToString(in.ClientToken))
		if err != nil || r.State != "dispatched" {
			t.Fatalf("dispatch not durable: %+v %v", r, err)
		}
		i := launched(in)
		instance = &i
		return nil, errors.New("response lost SECRET")
	}
	first, err := s.Up(context.Background(), m, p, UpOptions{Name: "smoke", OnDemand: true}, store, quiet)
	if err != nil || first.Status != "allocated" || api.launches != 1 {
		t.Fatalf("%+v %v launches=%d", first, err, api.launches)
	}
	r, err := store.Load(first.RequestID)
	if err != nil || r.State != "observed" || !reflect.DeepEqual(launchInput(r), original) {
		t.Fatalf("receipt changed: %+v %v", r, err)
	}
	// Restart: new service, no manifest/profile, no retained instance object in service.
	restarted := testService(api)
	got, err := restarted.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
	if err != nil || got.Status != "allocated" || api.launches != 1 {
		t.Fatalf("replay %+v %v launches=%d", got, err, api.launches)
	}
}
func TestUncertainLaunchNeverReallocates(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }
	api.run = func(*ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) { return nil, errors.New("lost") }
	r, err := s.Up(context.Background(), m, p, UpOptions{Name: "smoke", OnDemand: true}, store, quiet)
	if err == nil || r.Status != "outcome_unresolved" || !ValidRequest(r.RequestID) {
		t.Fatalf("%+v %v", r, err)
	}
	for range 2 {
		_, err = s.Up(context.Background(), config.Manifest{}, config.Profile{}, UpOptions{Resume: r.RequestID}, store, quiet)
		if err == nil {
			t.Fatal("invisible outcome claimed resolved")
		}
	}
	if api.launches != 1 {
		t.Fatalf("duplicate allocation: %d", api.launches)
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
			if variant == "same" {
				if err != nil || api.launches != 1 {
					t.Fatal(err, api.launches)
				}
			} else if err == nil || api.launches != 0 {
				t.Fatalf("changed parameters launched: %v %d", err, api.launches)
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
		if err != nil {
			t.Fatal(err)
		}
	}
	if api.launches != 1 {
		t.Fatal("multiple launches", api.launches)
	}
}
func TestPreMutationDiagnosticFailure(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }
	result, err := s.Up(context.Background(), m, p, UpOptions{Name: "smoke", OnDemand: true}, store, func(Receipt, string) error { return io.ErrClosedPipe })
	r, loadErr := store.Load(result.RequestID)
	if err == nil || api.launches != 0 || loadErr != nil || r.State != "prepared" {
		t.Fatalf("%+v %v %v", result, err, loadErr)
	}
}
func TestPostLaunchReceiptFailurePreservesIDs(t *testing.T) {
	s, m, p, store, api := setupUp(t)
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }
	api.run = func(in *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
		if err := os.Rename(store.Dir, store.Dir+"-moved"); err != nil {
			t.Fatal(err)
		}
		return &ec2.RunInstancesOutput{Instances: []types.Instance{launched(in)}}, nil
	}
	r, err := s.Up(context.Background(), m, p, UpOptions{Name: "smoke", OnDemand: true}, store, quiet)
	if err == nil || len(r.Instances) != 1 || r.Instances[0].ID != "i-12345678" || r.RequestID == "" {
		t.Fatalf("IDs lost: %+v %v", r, err)
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
	testutil.Write(t, path, testutil.Config+"profile_file='missing.toml'\n")
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
				testutil.Write(t, path, testutil.Config+"profile_file='missing.toml'\n")
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
