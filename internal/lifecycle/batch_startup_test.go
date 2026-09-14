package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// These controlled reads model AWS returning an allocated worker before its
// metadata, address or root attachment has finished converging. CreateFleet and
// the immutable S3 ledger remain the real integration fixture implementations.
type startupEC2 struct {
	*batchRunEC2
	mu          sync.Mutex
	reads       map[string]int
	volumeReads map[string]int
	instance    func(*types.Instance, int)
	volume      func(*types.Volume, int)
	probes      []string
	beforeProbe func(string)
}

func (a *startupEC2) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	out, err := a.batchRunEC2.DescribeInstances(ctx, in, opts...)
	if err != nil {
		return out, err
	}
	data, _ := json.Marshal(out)
	out = nil
	_ = json.Unmarshal(data, &out)
	a.mu.Lock()
	defer a.mu.Unlock()
	for rn := range out.Reservations {
		for n := range out.Reservations[rn].Instances {
			i := &out.Reservations[rn].Instances[n]
			id := aws.ToString(i.InstanceId)
			a.reads[id]++
			if a.instance != nil {
				a.instance(i, a.reads[id])
			}
		}
	}
	return out, nil
}

func (a *startupEC2) DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, opts ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	out, err := a.batchRunEC2.DescribeVolumes(ctx, in, opts...)
	if err != nil {
		return out, err
	}
	data, _ := json.Marshal(out)
	out = nil
	_ = json.Unmarshal(data, &out)
	a.mu.Lock()
	defer a.mu.Unlock()
	for n := range out.Volumes {
		v := &out.Volumes[n]
		id := aws.ToString(v.VolumeId)
		a.volumeReads[id]++
		if a.volume != nil {
			a.volume(v, a.volumeReads[id])
		}
	}
	return out, nil
}

type startupSSM struct {
	*batchRunSSM
	api *startupEC2
}

func (s *startupSSM) SendCommand(ctx context.Context, in *ssm.SendCommandInput, opts ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	s.api.mu.Lock()
	for _, id := range in.InstanceIds {
		if s.api.beforeProbe != nil {
			s.api.beforeProbe(id)
		}
		s.api.probes = append(s.api.probes, id)
	}
	s.api.mu.Unlock()
	return s.batchRunSSM.SendCommand(ctx, in, opts...)
}

func startupFixture(t *testing.T) (string, Dependencies, *startupEC2, LaunchSelection) {
	t.Helper()
	path, deps, api, ssmAPI, selection := batchRunFixture(t)
	observed := &startupEC2{batchRunEC2: api, reads: map[string]int{}, volumeReads: map[string]int{}}
	newService := deps.New
	deps.New = func(ctx context.Context, c config.Config) (*Service, error) {
		service, err := newService(ctx, c)
		service.API, service.SSM = observed, &startupSSM{batchRunSSM: ssmAPI, api: observed}
		return service, err
	}
	return path, deps, observed, selection
}

func TestBatchStartupWaitsForExactPinsBeforeReadiness(t *testing.T) {
	for _, lag := range []string{"public-address", "metadata", "attachment", "partial", "unknown"} {
		t.Run(lag, func(t *testing.T) {
			path, deps, api, selection := startupFixture(t)
			if lag == "partial" || lag == "unknown" {
				api.mode = lag
			}
			api.instance = func(i *types.Instance, reads int) {
				if reads <= 3 && lag != "attachment" {
					i.State.Name = types.InstanceStateNamePending
					if lag == "metadata" {
						i.MetadataOptions.State = types.InstanceMetadataOptionsStatePending
					} else {
						i.PublicIpAddress = nil
					}
				}
			}
			api.volume = func(v *types.Volume, reads int) {
				if lag == "attachment" && reads <= 3 {
					v.Attachments[0].State = types.VolumeAttachmentStateAttaching
				}
			}
			api.beforeProbe = func(id string) {
				volume := aws.ToString(api.instances[id].BlockDeviceMappings[0].Ebs.VolumeId)
				if api.reads[id] < 4 || (lag == "attachment" && api.volumeReads[volume] < 4) {
					t.Errorf("probe preceded full startup verification: %s instance reads=%d root reads=%d", id, api.reads[id], api.volumeReads[volume])
				}
			}
			r := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
			ready, exit := 2, 0
			if lag == "partial" {
				ready, exit = 1, 3
			} else if lag == "unknown" {
				ready, exit = 1, 1
			}
			if r.ExitCode != exit || r.ReadyCount != ready || r.FulfilledCount != ready || !reflect.DeepEqual(api.counts, []int{2}) || len(api.probes) != ready {
				t.Fatalf("startup did not converge independently: %+v calls=%v probes=%v", r, api.counts, api.probes)
			}
			if lag == "unknown" {
				if r.MissingCount != nil || r.RetryCommand != "" || r.Attempts[0].Status != "unknown" || len(r.Errors) == 0 {
					t.Fatalf("readiness erased original uncertainty: %+v", r)
				}
			} else if r.MissingCount == nil || *r.MissingCount != 2-ready {
				t.Fatalf("validated original bound was not recovered: %+v", r)
			}
		})
	}
}

func TestBatchStartupOnDemandWaitsForRootMappingBeforeProbe(t *testing.T) {
	path, deps, api, selection := startupFixture(t)
	selection.OnDemand, selection.Count = true, 1
	api.instance = func(i *types.Instance, reads int) {
		if reads <= 3 {
			i.State.Name = types.InstanceStateNamePending
			i.BlockDeviceMappings = nil
			i.RootDeviceName, i.RootDeviceType = nil, ""
		}
	}
	api.beforeProbe = func(id string) {
		volume := aws.ToString(api.instances[id].BlockDeviceMappings[0].Ebs.VolumeId)
		if api.reads[id] < 4 || api.volumeReads[volume] == 0 {
			t.Errorf("probe preceded observed mapping and exact root verification: %s", id)
		}
	}
	r := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
	if r.ExitCode != 0 || r.ReadyCount != 1 || r.MissingCount == nil || *r.MissingCount != 0 || r.Plan.Market != "on-demand" || len(r.Workers) != 1 || len(r.Workers[0].Volumes) != 1 || len(api.probes) != 1 || !reflect.DeepEqual(api.counts, []int{1}) {
		t.Fatalf("delayed root mapping prevented bounded direct On-Demand readiness: %+v calls=%v", r, api.counts)
	}
}

func TestBatchStartupTimeoutKeepsReadyPeerAndRoots(t *testing.T) {
	path, deps, api, selection := startupFixture(t)
	pending := "i-00000000000000064"
	api.instance = func(i *types.Instance, reads int) {
		if aws.ToString(i.InstanceId) == pending {
			i.PublicIpAddress = nil
			i.State.Name = types.InstanceStateNamePending
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	r := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
	if r.ExitCode != 4 || r.ReadyCount != 1 || r.FulfilledCount != 2 || r.MissingCount != nil || len(r.Workers) != 2 || !reflect.DeepEqual(api.counts, []int{2}) {
		t.Fatalf("pending worker starved peer or lost capacity evidence: %+v calls=%v", r, api.counts)
	}
	if !reflect.DeepEqual(api.probes, []string{"i-00000000000000065"}) {
		t.Fatalf("unverified worker received a probe: %v", api.probes)
	}
	for _, w := range r.Workers {
		if len(w.Volumes) != 1 || !w.Volumes[0].Root {
			t.Fatalf("timeout lost exact root mapping: %+v", w)
		}
	}
	// A later command can observe the same startup from shared evidence with a
	// fresh local cache; neither prepared records nor resume may dispatch again.
	api.mu.Lock()
	api.reads = map[string]int{}
	api.instance = func(i *types.Instance, reads int) {
		if reads <= 3 {
			i.MetadataOptions.State = types.InstanceMetadataOptionsStatePending
		}
	}
	api.probes = nil
	api.mu.Unlock()
	deps.Store = &Store{Dir: t.TempDir()}
	resume := LaunchSelection{Resume: r.RequestID}
	r = Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &resume}, deps, io.Discard).Batch
	if r.ExitCode != 0 || r.ReadyCount != 2 || r.MissingCount == nil || *r.MissingCount != 0 || len(api.counts) != 1 {
		t.Fatalf("shared resume failed to wait without redispatch: %+v calls=%v", r, api.counts)
	}
}

func TestBatchStartupStopsMismatchedWorkerWhilePeerProgresses(t *testing.T) {
	for _, mismatch := range []string{"image", "root", "group"} {
		t.Run(mismatch, func(t *testing.T) {
			path, deps, api, selection := startupFixture(t)
			bad := "i-00000000000000064"
			api.instance = func(i *types.Instance, reads int) {
				if aws.ToString(i.InstanceId) != bad {
					return
				}
				if reads == 1 {
					i.PublicIpAddress = nil
				} else if mismatch == "image" {
					i.ImageId = aws.String("ami-00000000000000000")
				} else if mismatch == "group" {
					for n := range i.Tags {
						if aws.ToString(i.Tags[n].Key) == "Group" {
							i.Tags[n].Value = aws.String("another-group")
						}
					}
				}
			}
			api.volume = func(v *types.Volume, reads int) {
				if mismatch == "root" && reads > 1 && aws.ToString(v.Attachments[0].InstanceId) == bad {
					v.Encrypted = aws.Bool(false)
				}
			}
			r := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
			if r.OK || r.ReadyCount != 1 || r.MissingCount != nil || !reflect.DeepEqual(api.probes, []string{"i-00000000000000065"}) || len(api.counts) != 1 || len(r.Workers[0].Volumes) != 1 {
				t.Fatalf("mismatched startup reached readiness or hid peer/evidence: %+v probes=%v", r, api.probes)
			}
		})
	}
}

func TestBatchStartupPendingWorkersDoNotStarveVerifiedQueue(t *testing.T) {
	path, deps, api, selection := startupFixture(t)
	selection.Count = 6
	api.instance = func(i *types.Instance, _ int) {
		if aws.ToString(i.InstanceId) < "i-00000000000000068" {
			i.MetadataOptions.State = types.InstanceMetadataOptionsStatePending
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	r := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
	if r.ExitCode != 4 || r.ReadyCount != 2 || len(api.probes) != 2 || r.MissingCount != nil || len(r.Workers) != 6 || !reflect.DeepEqual(api.counts, []int{6}) {
		t.Fatalf("four pending workers starved later verified peers: %+v calls=%v probes=%v", r, api.counts, api.probes)
	}
}

func TestBatchStartupExplicitSuccessorWaitsWithoutAnotherAllocation(t *testing.T) {
	path, deps, api, selection := startupFixture(t)
	api.mode = "partial"
	first := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
	if first.ExitCode != 3 {
		t.Fatalf("partial setup failed: %+v", first)
	}
	api.mode = "full"
	api.instance = func(i *types.Instance, reads int) {
		if aws.ToString(i.InstanceId) == "i-000000000000000c8" && reads <= 3 {
			i.MetadataOptions.State = types.InstanceMetadataOptionsStatePending
		}
	}
	retry := LaunchSelection{RetryMissing: first.RequestID, After: first.Attempts[0].AttemptID}
	r := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &retry}, deps, io.Discard).Batch
	if r.ExitCode != 0 || r.ReadyCount != 2 || r.MissingCount == nil || *r.MissingCount != 0 || !reflect.DeepEqual(api.counts, []int{2, 1}) {
		t.Fatalf("successor startup lost original workers or redispatched: %+v calls=%v", r, api.counts)
	}
}

type startupResponseFailure struct {
	S3LaunchAPI
}

func (s startupResponseFailure) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if strings.HasSuffix(aws.ToString(in.Key), "/response.json") {
		return nil, errors.New("response persistence unavailable")
	}
	return s.S3LaunchAPI.PutObject(ctx, in, opts...)
}

func TestBatchStartupDoesNotEraseOriginalPersistenceFailure(t *testing.T) {
	path, deps, api, selection := startupFixture(t)
	api.instance = func(i *types.Instance, reads int) {
		if reads <= 3 {
			i.PublicIpAddress = nil
		}
	}
	newService := deps.New
	deps.New = func(ctx context.Context, c config.Config) (*Service, error) {
		service, err := newService(ctx, c)
		service.LaunchRecords = startupResponseFailure{service.LaunchRecords}
		return service, err
	}
	r := Run(context.Background(), path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
	if r.OK || r.ReadyCount != 2 || r.MissingCount != nil || r.RetryCommand != "" || len(r.Errors) == 0 || len(api.counts) != 1 {
		t.Fatalf("ready workers hid original failed response persistence: %+v calls=%v", r, api.counts)
	}
}
