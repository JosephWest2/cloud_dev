package expirycleanup

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func historicalInstance() types.Instance {
	i := instance(1)
	i.State = &types.InstanceState{Name: "terminated", Code: aws.Int32(48)}
	i.BlockDeviceMappings = nil
	return i
}

func TestSuccessfulCleanupThenHistoricalRowsRemainBenign(t *testing.T) {
	for _, dropMetadata := range []bool{false, true} {
		t.Run(fmt.Sprint(dropMetadata), func(t *testing.T) {
			f := fake(instance(1))
			var events, prepared atomic.Int32
			s := service(t, f, func(d *Dependencies, _ *Limits) {
				d.Sink = sinkFunc(func(_ context.Context, e expiry.Event) error {
					events.Add(1)
					if e.Kind == "termination_prepared" {
						prepared.Add(1)
					}
					return nil
				})
			})
			first := run(t, s, false)
			if !first.OK || first.CleanedCount != 1 || len(f.terminations) != 1 || len(f.volumes) != 1 {
				t.Fatal(first)
			}
			// Simulate EC2 dropping mappings after a successfully verified cleanup.
			f.mu.Lock()
			i := f.instances[iid(1)]
			i.BlockDeviceMappings = nil
			if dropMetadata {
				i.RootDeviceName = nil
				i.RootDeviceType = ""
			}
			f.instances[iid(1)] = i
			f.mu.Unlock()
			for _, dry := range []bool{false, true, false} {
				beforeEvents := events.Load()
				beforeReads := len(f.discovery)
				r := run(t, s, dry)
				o := outcome(t, r, iid(1))
				if !r.OK || !r.Complete || !r.ScanComplete || r.ExitCode != 0 || r.Code != "cleanup_no_candidates" || r.CandidateCount != 0 || r.ScannedCount != 1 || r.CleanedCount != 0 || len(r.Errors) != 0 {
					t.Fatal(r)
				}
				wantTerminal, wantReads := 1, 2
				if dry {
					wantTerminal = 0
					wantReads = 1
				}
				if r.TerminatedCount != wantTerminal || len(f.discovery)-beforeReads != wantReads || o.Status != "already_terminated" || o.RootDeletion != "unavailable" || o.Reason != expiry.AlreadyTerminated || o.Eligible || len(o.Volumes) != 0 || len(o.Errors) != 0 {
					t.Fatal(r)
				}
				if len(f.terminations) != 1 || len(f.volumes) != 1 || prepared.Load() != 1 {
					t.Fatalf("unexpected repeat calls: termination=%v volume=%v prepared=%d", f.terminations, f.volumes, prepared.Load())
				}
				if dry && events.Load() != beforeEvents {
					t.Fatal("dry-run wrote events")
				}
			}
		})
	}
}

func TestHistoricalRowsDoNotMakeHealthyPeersPartial(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			f := fake(historicalInstance(), instance(2))
			r := run(t, service(t, f), dry)
			history := outcome(t, r, iid(1))
			if !r.OK || !r.Complete || r.Code != "cleanup_complete" || r.CandidateCount != 1 || history.Status != "already_terminated" || history.RootDeletion != "unavailable" || len(history.Errors) != 0 {
				t.Fatal(r)
			}
			if dry {
				if r.TerminatedCount != 0 || r.CleanedCount != 0 || len(f.terminations) != 0 || len(f.volumes) != 0 {
					t.Fatal(r)
				}
			} else if r.TerminatedCount != 2 || r.CleanedCount != 1 || len(f.terminations) != 1 || f.terminations[0] != iid(2) || len(f.volumes) != 1 || f.volumes[0] != vid(2) {
				t.Fatal(r)
			}
		})
	}
}

func TestAbsentMappingsStillFailForLiveWorkers(t *testing.T) {
	for _, state := range []types.InstanceStateName{"pending", "running", "stopping", "stopped", "shutting-down"} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%t", state, dry), func(t *testing.T) {
				i := historicalInstance()
				i.State = &types.InstanceState{Name: state}
				f := fake(i)
				r := run(t, service(t, f), dry)
				if r.OK || r.Complete || r.CleanedCount != 0 || len(f.terminations) != 0 || len(f.volumes) != 0 {
					t.Fatal(r)
				}
				requireCode(t, outcome(t, r, iid(1)), "root_volume_unverified")
			})
		}
	}
}

func TestHistoricalExceptionDoesNotHideMappingOrScopeErrors(t *testing.T) {
	tests := []struct {
		name   string
		change func(*types.Instance)
		code   string
	}{
		{"malformed mapping", func(i *types.Instance) {
			i.BlockDeviceMappings = []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/xvda")}}
		}, "root_volume_unverified"},
		{"invalid volume ID", func(i *types.Instance) {
			i.BlockDeviceMappings = instance(1).BlockDeviceMappings
			i.BlockDeviceMappings[0].Ebs.VolumeId = aws.String("invalid")
		}, "root_volume_unverified"},
		{"retained root", func(i *types.Instance) {
			i.BlockDeviceMappings = instance(1).BlockDeviceMappings
			i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
		}, "root_volume_retained"},
		{"unknown root flag", func(i *types.Instance) {
			i.BlockDeviceMappings = instance(1).BlockDeviceMappings
			i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = nil
		}, "root_volume_unverified"},
		{"non-root mapping only", func(i *types.Instance) {
			i.BlockDeviceMappings = instance(1).BlockDeviceMappings
			i.BlockDeviceMappings[0].DeviceName = aws.String("/dev/xvdb")
			i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = nil
		}, "root_volume_unverified"},
		{"contradictory root type", func(i *types.Instance) { i.RootDeviceType = "instance-store" }, "root_volume_unverified"},
		{"wrong owner", func(i *types.Instance) { i.Tags[2].Value = aws.String("other") }, "scope_mismatch"},
		{"invalid expiry", func(i *types.Instance) { i.Tags[3].Value = nil }, "expiry_invalid"},
		{"contradictory state", func(i *types.Instance) { i.State.Code = aws.Int32(16) }, "state_unknown"},
	}
	for _, tc := range tests {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%t", tc.name, dry), func(t *testing.T) {
				i := historicalInstance()
				tc.change(&i)
				f := fake(i)
				r := run(t, service(t, f), dry)
				if r.OK || r.Complete || r.CleanedCount != 0 || len(f.terminations) != 0 || len(f.volumes) != 0 {
					t.Fatal(r)
				}
				requireCode(t, outcome(t, r, iid(1)), tc.code)
			})
		}
	}
}

func TestHistoricalRowsStillRequireExactRecheck(t *testing.T) {
	tests := []struct {
		name     string
		response func() (*ec2.DescribeInstancesOutput, error)
		code     string
	}{
		{"missing", func() (*ec2.DescribeInstancesOutput, error) { return &ec2.DescribeInstancesOutput{}, nil }, "resource_unverified"},
		{"foreign", func() (*ec2.DescribeInstancesOutput, error) {
			i := historicalInstance()
			i.InstanceId = aws.String(iid(9))
			return page(i), nil
		}, "resource_unverified"},
		{"duplicate", func() (*ec2.DescribeInstancesOutput, error) {
			return page(historicalInstance(), historicalInstance()), nil
		}, "resource_unverified"},
		{"partial error", func() (*ec2.DescribeInstancesOutput, error) {
			return page(historicalInstance()), errors.New("incomplete")
		}, "resource_unverified"},
		{"scope drift", func() (*ec2.DescribeInstancesOutput, error) {
			i := historicalInstance()
			i.Tags[2].Value = aws.String("other")
			return page(i), nil
		}, "scope_mismatch"},
		{"state regression", func() (*ec2.DescribeInstancesOutput, error) {
			i := historicalInstance()
			i.State = &types.InstanceState{Name: "running"}
			return page(i), nil
		}, "resource_unverified"},
		{"mapping appears", func() (*ec2.DescribeInstancesOutput, error) {
			i := historicalInstance()
			i.BlockDeviceMappings = instance(1).BlockDeviceMappings
			return page(i), nil
		}, "root_volume_unverified"},
		{"malformed mapping", func() (*ec2.DescribeInstancesOutput, error) {
			i := historicalInstance()
			i.BlockDeviceMappings = []types.InstanceBlockDeviceMapping{{}}
			return page(i), nil
		}, "root_volume_unverified"},
		{"contradictory root type", func() (*ec2.DescribeInstancesOutput, error) {
			i := historicalInstance()
			i.RootDeviceType = "instance-store"
			return page(i), nil
		}, "root_volume_unverified"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fake()
			f.describe = func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				if len(in.InstanceIds) == 0 {
					return page(historicalInstance()), nil
				}
				return tc.response()
			}
			r := run(t, service(t, f), false)
			if r.OK || r.Complete || r.CleanedCount != 0 || len(f.terminations) != 0 || len(f.volumes) != 0 {
				t.Fatal(r)
			}
			requireCode(t, outcome(t, r, iid(1)), tc.code)
		})
	}
}
