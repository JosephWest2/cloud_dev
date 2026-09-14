package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"io"
	"testing"
	"time"
)

type inventoryAPI struct {
	*ec2.Client
	fail       bool
	terminated bool
}

func (a *inventoryAPI) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if a.fail {
		return nil, errors.New("SECRET SDK error")
	}
	i := types.Instance{
		InstanceId: aws.String("i-12345678"),
		State:      &types.InstanceState{Name: types.InstanceStateNameRunning},
		Tags: []types.Tag{
			{Key: aws.String("ManagedBy"), Value: aws.String("devbox")},
			{Key: aws.String("Deployment"), Value: aws.String("test")},
			{Key: aws.String("Owner"), Value: aws.String("test-owner")},
			{Key: aws.String("Name"), Value: aws.String("smoke")},
		},
		RootDeviceName:      aws.String("/dev/sda1"),
		BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-12345678"), DeleteOnTermination: aws.Bool(true)}}},
	}
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String("123456789012"), Instances: []types.Instance{i}}}}, nil
}

func (a *inventoryAPI) TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	a.terminated = true
	return nil, errors.New("SECRET lost termination response")
}
func TestLifecycleJSONAndTimeoutIdentifiers(t *testing.T) {
	for _, command := range []string{"ls", "down"} {
		for _, fail := range []bool{false, true} {
			t.Run(command+map[bool]string{true: "fail", false: "ok"}[fail], func(t *testing.T) {
				api := &inventoryAPI{fail: fail}
				deps := lifecycle.Dependencies{New: func(_ context.Context, c config.Config) (*lifecycle.Service, error) {
					return &lifecycle.Service{API: api, Scope: c, PollInterval: time.Second}, nil
				}}
				args := []string{command, "--config", testutil.Setup(t), "--json", "--timeout", "5ms"}
				if command == "down" {
					args = append(args, "smoke")
				}
				var out, diag bytes.Buffer
				code := RunWithLifecycle(context.Background(), args, &out, &diag, doctor.Dependencies{}, deps)
				var r lifecycle.Result
				d := json.NewDecoder(&out)
				if err := d.Decode(&r); err != nil || d.Decode(new(any)) != io.EOF {
					t.Fatal("invalid JSON", err)
				}
				if bytes.Contains(diag.Bytes(), []byte("SECRET")) || bytes.Contains(out.Bytes(), []byte("SECRET")) {
					t.Fatal("SDK diagnostic leaked")
				}
				if command == "down" && !fail {
					if code != 4 || len(r.Instances) != 1 || r.Instances[0].ID != "i-12345678" || r.Instances[0].Volumes[0].ID != "vol-12345678" {
						t.Fatalf("missing timeout identities: %+v", r)
					}
				} else if fail && code != 1 {
					t.Fatal(code)
				} else if command == "ls" && !fail && (code != 1 || len(r.Instances) != 1 || r.Instances[0].Readiness != "unknown") {
					t.Fatal(code)
				}
			})
		}
	}
}
func TestLifecycleUsageFailsBeforeAWS(t *testing.T) {
	for _, args := range [][]string{{"up", "agent", "--name", "smoke"}, {"up", "agent", "--spot", "--name", "smoke"}, {"up", "--resume", "0123456789abcdef0123456789abcdef", "--name", "SECRET"}, {"up", "agent", "--on-demand", "--name", "i-12345678"}, {"ls", "--name", "SECRET"}, {"down"}} {
		var out, diag bytes.Buffer
		args = append(args, "--json", "--config", testutil.Setup(t))
		code := RunWithLifecycle(context.Background(), args, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{New: func(context.Context, config.Config) (*lifecycle.Service, error) {
			t.Fatal("invalid usage reached AWS")
			return nil, nil
		}})
		if code != 2 || !json.Valid(out.Bytes()) || bytes.Contains(out.Bytes(), []byte("SECRET")) {
			t.Fatalf("invalid usage: %d %s", code, &out)
		}
	}
}

func TestInteractiveJSONRejectedBeforeAWS(t *testing.T) {
	for _, command := range []string{"ssh", "proxy"} {
		var out, diag bytes.Buffer
		code := RunWithLifecycle(context.Background(), []string{command, "i-12345678", "--json"}, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{New: func(context.Context, config.Config) (*lifecycle.Service, error) {
			t.Fatal("interactive JSON reached AWS")
			return nil, nil
		}})
		if code != 2 || !json.Valid(out.Bytes()) || !bytes.Contains(out.Bytes(), []byte("reject --json")) {
			t.Fatalf("%d %s", code, &out)
		}
	}
}
