package lifecycle

import (
	"context"
	"errors"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"testing"
	"time"
)

type fakeEC2 struct {
	describe               func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	volumes                func(*ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	terminate              func(*ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error)
	run                    func(*ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error)
	launches, terminations int
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f.describe(in)
}
func (f *fakeEC2) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return f.volumes(in)
}
func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminations++
	if f.terminate != nil {
		return f.terminate(in)
	}
	return &ec2.TerminateInstancesOutput{}, nil
}
func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.launches++
	return f.run(in)
}
func testService(api *fakeEC2) *Service {
	return &Service{API: api, Scope: config.Config{ExpectedAccount: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "test-owner"}, PollInterval: time.Nanosecond}
}
func worker(id string) types.Instance {
	return types.Instance{InstanceId: aws.String(id), ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeC7i2xlarge, State: &types.InstanceState{Name: types.InstanceStateNameRunning}, RootDeviceName: aws.String("/dev/sda1"), Tags: []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String("test")}, {Key: aws.String("Owner"), Value: aws.String("test-owner")}, {Key: aws.String("Name"), Value: aws.String("smoke")}}, BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-12345678"), DeleteOnTermination: aws.Bool(true)}}}}
}
func inventory(items ...types.Instance) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String("123456789012"), Instances: items}}}
}
func TestDownRejectsUnsafeTargets(t *testing.T) {
	for _, variant := range []string{"unmanaged", "owner", "deployment", "account", "ambiguous", "scope_changed", "name_changed"} {
		for _, target := range []string{"smoke", "i-12345678"} {
			if variant == "ambiguous" && target != "smoke" {
				continue
			}
			t.Run(variant+target, func(t *testing.T) {
				calls := 0
				api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					calls++
					i := worker("i-12345678")
					out := inventory(i)
					switch variant {
					case "unmanaged":
						out.Reservations[0].Instances[0].Tags[0].Value = aws.String("other")
					case "owner":
						out.Reservations[0].Instances[0].Tags[2].Value = aws.String("other")
					case "deployment":
						out.Reservations[0].Instances[0].Tags[1].Value = aws.String("other")
					case "account":
						out.Reservations[0].OwnerId = aws.String("999999999999")
					case "ambiguous":
						out = inventory(i, worker("i-87654321"))
					case "scope_changed":
						if calls > 1 {
							out.Reservations[0].Instances[0].Tags[2].Value = aws.String("other")
						}
					case "name_changed":
						if target != "smoke" {
							out.Reservations[0].OwnerId = aws.String("999999999999")
						} else if calls > 1 {
							out.Reservations[0].Instances[0].Tags[3].Value = aws.String("other")
						}
					}
					return out, nil
				}}
				_, err := testService(api).Down(context.Background(), target)
				if err == nil || api.terminations != 0 {
					t.Fatalf("unsafe termination: err=%v calls=%d", err, api.terminations)
				}
			})
		}
	}
}
func TestDownLostResponseAndRootDeletion(t *testing.T) {
	api := &fakeEC2{}
	api.describe = func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		i := worker("i-12345678")
		if api.terminations > 0 {
			i.State.Name = types.InstanceStateNameTerminated
			i.BlockDeviceMappings = nil
		}
		return inventory(i), nil
	}
	api.terminate = func(in *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
		if len(in.InstanceIds) != 1 || in.InstanceIds[0] != "i-12345678" {
			t.Fatal(in)
		}
		return nil, errors.New("lost response SECRET")
	}
	api.volumes = func(in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
		if len(in.VolumeIds) != 1 || in.VolumeIds[0] != "vol-12345678" {
			t.Fatal(in)
		}
		return nil, &smithy.GenericAPIError{Code: "InvalidVolume.NotFound"}
	}
	r, err := testService(api).Down(context.Background(), "smoke")
	if err != nil || r.Status != "terminated" || r.Instances[0].RootDeletion != "deleted" || api.terminations != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = testService(api).Down(context.Background(), "smoke")
	if err != nil || r.Status != "already_terminated" || r.Instances[0].RootDeletion != "unavailable" || api.terminations != 1 {
		t.Fatalf("repeated %+v %v", r, err)
	}
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(), nil }
	r, err = testService(api).Down(context.Background(), "smoke")
	if err != nil || r.Status != "no_managed_match" || len(r.Instances) != 0 {
		t.Fatalf("absent %+v %v", r, err)
	}
}
func TestDownTimeoutRetainsIDs(t *testing.T) {
	api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return inventory(worker("i-12345678")), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	s := testService(api)
	s.PollInterval = time.Second
	r, err := s.Down(ctx, "smoke")
	if !errors.Is(err, context.DeadlineExceeded) || r.Instances[0].ID != "i-12345678" || r.Instances[0].Volumes[0].ID != "vol-12345678" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestInventoryPagination(t *testing.T) {
	calls := 0
	api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		calls++
		if len(in.Filters) != 3 {
			t.Fatal("missing scope filters")
		}
		if calls == 1 {
			out := inventory(worker("i-12345678"))
			out.NextToken = aws.String("page2")
			return out, nil
		}
		if aws.ToString(in.NextToken) != "page2" {
			t.Fatal("missing next token")
		}
		return inventory(worker("i-87654321")), nil
	}}
	got, err := testService(api).List(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestDownRetainedRoot(t *testing.T) {
	api := &fakeEC2{}
	api.describe = func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		i := worker("i-12345678")
		i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
		if api.terminations > 0 {
			i.State.Name = types.InstanceStateNameTerminated
		}
		return inventory(i), nil
	}
	r, err := testService(api).Down(context.Background(), "smoke")
	if err != nil || r.Instances[0].RootDeletion != "retained" {
		t.Fatalf("%+v %v", r, err)
	}
}
