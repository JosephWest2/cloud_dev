package lifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	st "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/pelletier/go-toml/v2"
)

type batchRunSSM struct {
	*fakeSSM
	mu       sync.Mutex
	failedID string
}

func (s *batchRunSSM) SendCommand(ctx context.Context, in *ssm.SendCommandInput, opts ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.InstanceIds[0] == s.failedID {
		return nil, errors.New("SECRET probe denial")
	}
	return s.fakeSSM.SendCommand(ctx, in, opts...)
}
func (s *batchRunSSM) GetCommandInvocation(ctx context.Context, in *ssm.GetCommandInvocationInput, opts ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeSSM.GetCommandInvocation(ctx, in, opts...)
}

type batchRunEC2 struct {
	*ec2.Client
	mu              sync.Mutex
	account         string
	prototype       types.Instance
	prototypeVolume types.Volume
	instances       map[string]types.Instance
	volumes         map[string]types.Volume
	counts          []int
	mode            string
	before          func(*ec2.CreateFleetInput)
}

func (a *batchRunEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := []types.Instance{}
	for id, instance := range a.instances {
		matches := len(in.InstanceIds) == 0
		for _, wanted := range in.InstanceIds {
			matches = matches || wanted == id
		}
		for _, filter := range in.Filters {
			key := strings.TrimPrefix(aws.ToString(filter.Name), "tag:")
			matches = matches && len(filter.Values) == 1 && tagsOf(instance)[key] == filter.Values[0]
		}
		if matches {
			result = append(result, instance)
		}
	}
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(a.account), Instances: result}}}, nil
}
func (a *batchRunEC2) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := &ec2.DescribeVolumesOutput{Volumes: []types.Volume{}}
	for _, id := range in.VolumeIds {
		if v, found := a.volumes[id]; found {
			result.Volumes = append(result.Volumes, v)
		}
	}
	return result, nil
}
func (a *batchRunEC2) DescribeFleets(context.Context, *ec2.DescribeFleetsInput, ...func(*ec2.Options)) (*ec2.DescribeFleetsOutput, error) {
	// Instant Fleet records may expire; complete shared responses remain history.
	return &ec2.DescribeFleetsOutput{Fleets: []types.FleetData{}}, nil
}
func (a *batchRunEC2) CreateFleet(_ context.Context, in *ec2.CreateFleetInput, _ ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.before != nil {
		a.before(in)
	}
	count := int(aws.ToInt32(in.TargetCapacitySpecification.TotalTargetCapacity))
	a.counts = append(a.counts, count)
	fulfilled := count
	if a.mode == "partial" || a.mode == "unknown" {
		fulfilled = 1
	}
	if a.mode == "zero" {
		fulfilled = 0
	}
	choice := in.LaunchTemplateConfigs[0].Overrides[0]
	tags := []types.Tag{}
	for _, spec := range in.TagSpecifications {
		if spec.ResourceType == types.ResourceTypeInstance {
			tags = spec.Tags
		}
	}
	ids := []string{}
	for n := 0; n < fulfilled; n++ {
		id, volumeID := fmt.Sprintf("i-%017x", 100*len(a.counts)+n), fmt.Sprintf("vol-%017x", 100*len(a.counts)+n)
		ids = append(ids, id)
		var instance types.Instance
		var volume types.Volume
		data, _ := json.Marshal(a.prototype)
		_ = json.Unmarshal(data, &instance)
		data, _ = json.Marshal(a.prototypeVolume)
		_ = json.Unmarshal(data, &volume)
		instance.InstanceId, instance.ImageId, instance.InstanceType = aws.String(id), choice.ImageId, choice.InstanceType
		instance.SubnetId, instance.Placement.AvailabilityZone = choice.SubnetId, choice.AvailabilityZone
		instance.NetworkInterfaces[0].SubnetId = choice.SubnetId
		instance.InstanceLifecycle = types.InstanceLifecycleTypeSpot
		if in.TargetCapacitySpecification.DefaultTargetCapacityType == types.DefaultTargetCapacityTypeOnDemand {
			instance.InstanceLifecycle = ""
		}
		spec := in.LaunchTemplateConfigs[0].LaunchTemplateSpecification
		instance.Tags = append(append([]types.Tag{}, tags...), types.Tag{Key: aws.String("aws:ec2launchtemplate:id"), Value: spec.LaunchTemplateId}, types.Tag{Key: aws.String("aws:ec2launchtemplate:version"), Value: spec.Version})
		instance.BlockDeviceMappings[0].Ebs.VolumeId = aws.String(volumeID)
		volume.VolumeId, volume.Tags, volume.AvailabilityZone = aws.String(volumeID), tags, choice.AvailabilityZone
		volume.Size = choice.BlockDeviceMappings[0].Ebs.VolumeSize
		volume.Attachments[0].InstanceId, volume.Attachments[0].VolumeId = aws.String(id), aws.String(volumeID)
		a.instances[id], a.volumes[volumeID] = instance, volume
	}
	out := &ec2.CreateFleetOutput{FleetId: aws.String(fmt.Sprintf("fleet-00000000-0000-0000-0000-%012d", len(a.counts))), Instances: []types.CreateFleetInstance{}, Errors: []types.CreateFleetError{}}
	if len(ids) > 0 {
		out.Instances = append(out.Instances, types.CreateFleetInstance{InstanceIds: ids, InstanceType: choice.InstanceType, SubnetId: choice.SubnetId, AvailabilityZone: choice.AvailabilityZone, Lifecycle: types.InstanceLifecycle(in.TargetCapacitySpecification.DefaultTargetCapacityType)})
	}
	if fulfilled < count {
		out.Errors = []types.CreateFleetError{{ErrorCode: aws.String("InsufficientInstanceCapacity"), ErrorMessage: aws.String("SECRET pool detail"), Lifecycle: types.InstanceLifecycle(in.TargetCapacitySpecification.DefaultTargetCapacityType), LaunchTemplateAndOverrides: &types.LaunchTemplateAndOverridesResponse{Overrides: &types.FleetLaunchTemplateOverrides{InstanceType: choice.InstanceType, SubnetId: choice.SubnetId, AvailabilityZone: choice.AvailabilityZone}}}}
	}
	if a.mode == "unknown" {
		return out, errors.New("SECRET lost response")
	}
	return out, nil
}

func batchRunFixture(t *testing.T) (string, Dependencies, *batchRunEC2, *batchRunSSM, LaunchSelection) {
	t.Helper()
	c, m, p, selection := batchFixture(t)
	_, _, _, prototype := fleetWorkersFixture(t, false)
	ssmAPI := &batchRunSSM{fakeSSM: &fakeSSM{ping: st.PingStatusOnline, document: `{"schemaVersion":"2.2"}`, output: `{"schema_version":1,"bootstrap":"complete","host_key":"` + hostKey + `"}`}}
	digest := sha256.Sum256([]byte(ssmAPI.document))
	m.Readiness = config.Document{Name: "probe", Version: "1", ContentSHA256: hex.EncodeToString(digest[:])}
	dir := filepath.Dir(c.Manifest)
	c.ProfileFile = filepath.Join(dir, "profile.toml")
	path := filepath.Join(dir, "config.toml")
	for _, file := range []struct {
		path  string
		value any
		toml  bool
	}{{c.Manifest, m, false}, {c.ProfileFile, p, true}, {path, c, true}} {
		var data []byte
		var err error
		if file.toml {
			data, err = toml.Marshal(file.value)
		} else {
			data, err = json.Marshal(file.value)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file.path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	api := &batchRunEC2{account: c.ExpectedAccount, prototype: prototype.instances[0].out.Reservations[0].Instances[0], prototypeVolume: prototype.volumes[0].out.Volumes[0], instances: map[string]types.Instance{}, volumes: map[string]types.Volume{}}
	storage := &launchMemoryS3{objects: map[string][]byte{}, getErrors: map[string]error{}}
	store := Store{Dir: t.TempDir()}
	deps := Dependencies{Store: &store, New: func(_ context.Context, scope config.Config) (*Service, error) {
		return &Service{API: api, Fleet: api, LaunchRecords: storage, SSM: ssmAPI, Scope: scope, PollInterval: time.Millisecond, VerifyFoundation: func(context.Context, config.Manifest, config.Profile) error { return nil }}, nil
	}}
	return path, deps, api, ssmAPI, selection
}

func TestBatchRunPreviewCapacityAndReadinessAreIndependent(t *testing.T) {
	for _, mode := range []string{"full", "partial", "zero", "unknown", "readiness-failure", "on-demand"} {
		t.Run(mode, func(t *testing.T) {
			path, deps, api, ssmAPI, selection := batchRunFixture(t)
			api.mode = mode
			if mode == "on-demand" {
				selection.OnDemand = true
			}
			if mode == "readiness-failure" {
				ssmAPI.failedID = "i-00000000000000064"
			}
			var diagnostics bytes.Buffer
			api.before = func(in *ec2.CreateFleetInput) {
				for _, field := range []string{"launch profile=agent", "count=2", "group=\"smoke-batch\"", "eligible_types=", "eligible_subnets_azs=", "saved request"} {
					if !strings.Contains(diagnostics.String(), field) {
						t.Errorf("dispatch preceded preview/recovery: missing %s", field)
					}
				}
			}
			r := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, &diagnostics).Batch
			wantExit, wantReady, wantFulfilled := 0, 2, 2
			switch mode {
			case "partial":
				wantExit, wantReady, wantFulfilled = 3, 1, 1
			case "zero":
				wantExit, wantReady, wantFulfilled = 1, 0, 0
			case "unknown":
				wantExit, wantReady, wantFulfilled = 1, 1, 1
			case "readiness-failure":
				wantExit, wantReady = 3, 1
			}
			if r == nil || r.ExitCode != wantExit || r.ReadyCount != wantReady || r.FulfilledCount != wantFulfilled || len(r.Workers) != wantFulfilled || !reflect.DeepEqual(api.counts, []int{2}) {
				t.Fatalf("wrong independent outcomes: %+v counts=%v", r, api.counts)
			}
			if mode == "unknown" && r.MissingCount != nil {
				t.Fatal("uncertainty gained missing count")
			}
			if mode == "on-demand" && r.Plan.Market != "on-demand" {
				t.Fatal("explicit market ignored")
			}
			encoded, _ := json.Marshal(r)
			if strings.Contains(string(encoded), "SECRET") {
				t.Fatal("raw diagnostics exposed")
			}
		})
	}
}

func TestBatchRunResumeAfterCacheLossAndRetryRemainSeparate(t *testing.T) {
	path, deps, api, _, selection := batchRunFixture(t)
	api.mode = "partial"
	first := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
	if first.ExitCode != 3 {
		t.Fatalf("initial partial failed: %+v", first)
	}
	deps.Store = &Store{Dir: t.TempDir()}
	resume := LaunchSelection{Resume: first.RequestID}
	observed := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &resume}, deps, io.Discard).Batch
	if observed.ExitCode != 3 || observed.FulfilledCount != 1 || len(api.counts) != 1 {
		t.Fatalf("resume allocated or lost history: %+v", observed)
	}
	api.mode = "full"
	retry := LaunchSelection{RetryMissing: first.RequestID, After: first.Attempts[0].AttemptID}
	completed := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &retry}, deps, io.Discard).Batch
	if completed.ExitCode != 0 || completed.FulfilledCount != 2 || !reflect.DeepEqual(api.counts, []int{2, 1}) {
		t.Fatalf("retry count wrong: %+v counts=%v", completed, api.counts)
	}
	_ = Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &retry}, deps, io.Discard)
	if len(api.counts) != 2 {
		t.Fatal("repeated retry allocated")
	}
}

func TestBatchRunObservationSurvivesProfileLossAndLowerCap(t *testing.T) {
	for _, change := range []string{"profile-loss", "lower-cap"} {
		t.Run(change, func(t *testing.T) {
			path, deps, api, _, selection := batchRunFixture(t)
			api.mode = "partial"
			first := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
			if first.ExitCode != 3 {
				t.Fatalf("initial partial: %+v", first)
			}
			c, err := config.Load(path, config.Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if change == "profile-loss" {
				if err = os.Remove(c.ProfileFile); err != nil {
					t.Fatal(err)
				}
			} else {
				c.MaxCount = 1
				data, marshalErr := toml.Marshal(c)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if err = os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			resume := LaunchSelection{Resume: first.RequestID}
			out := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &resume}, deps, io.Discard).Batch
			if out.ExitCode != 3 || out.FulfilledCount != 1 || out.ReadyCount != 1 {
				t.Fatalf("observation acquired launch prerequisite: %+v", out)
			}
			retry := LaunchSelection{RetryMissing: first.RequestID, After: first.Attempts[0].AttemptID}
			out = Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &retry}, deps, io.Discard).Batch
			if out.ExitCode != 2 || len(api.counts) != 1 || !strings.Contains(out.ResumeCommand, first.RequestID) {
				t.Fatalf("invalid retry allocated or lost recovery: %+v calls=%v", out, api.counts)
			}
		})
	}
}

func TestBatchRunFailedPreviewCannotDispatch(t *testing.T) {
	path, deps, api, _, selection := batchRunFixture(t)
	out := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, &batchProgressFailure{}).Batch
	if out == nil || out.Code != "output_unavailable" || out.ExitCode != 1 || len(api.counts) != 0 || out.ResumeCommand == "" {
		t.Fatalf("failed preview dispatched or lost request: %+v calls=%v", out, api.counts)
	}
}
