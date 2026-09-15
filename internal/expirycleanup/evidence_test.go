package expirycleanup

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestLiveMappingAttachmentEvidence(t *testing.T) {
	tests := []struct {
		name   string
		change func(*types.EbsInstanceBlockDevice)
		valid  bool
	}{
		{"omitted", func(b *types.EbsInstanceBlockDevice) { b.Status = ""; b.VolumeOwnerId = nil }, true},
		{"attached and owned", func(b *types.EbsInstanceBlockDevice) {
			b.Status = "attached"
			b.VolumeOwnerId = aws.String(scope.Account)
		}, true},
		{"attaching", func(b *types.EbsInstanceBlockDevice) { b.Status = "attaching" }, false},
		{"detaching", func(b *types.EbsInstanceBlockDevice) { b.Status = "detaching" }, false},
		{"detached", func(b *types.EbsInstanceBlockDevice) { b.Status = "detached" }, false},
		{"unknown status", func(b *types.EbsInstanceBlockDevice) { b.Status = "bogus" }, false},
		{"foreign owner", func(b *types.EbsInstanceBlockDevice) { b.VolumeOwnerId = aws.String("999999999999") }, false},
		{"empty supplied owner", func(b *types.EbsInstanceBlockDevice) { b.VolumeOwnerId = aws.String("") }, false},
	}
	for _, stage := range []string{"discovery", "recheck"} {
		for _, tc := range tests {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				i := instance(1)
				i.BlockDeviceMappings[0].Ebs.Status = "attached"
				i.BlockDeviceMappings[0].Ebs.VolumeOwnerId = aws.String(scope.Account)
				if stage == "discovery" {
					tc.change(i.BlockDeviceMappings[0].Ebs)
				}
				f := fake(i, instance(2))
				f.describe = func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					f.mu.Lock()
					defer f.mu.Unlock()
					if len(in.InstanceIds) == 0 {
						return page(cloneInstance(f.instances[iid(1)]), cloneInstance(f.instances[iid(2)])), nil
					}
					current := cloneInstance(f.instances[in.InstanceIds[0]])
					if stage == "recheck" && in.InstanceIds[0] == iid(1) {
						tc.change(current.BlockDeviceMappings[0].Ebs)
					}
					return page(current), nil
				}
				r := run(t, service(t, f), false)
				o := outcome(t, r, iid(1))
				if len(o.Volumes) != 1 || o.Volumes[0].ID != vid(1) {
					t.Fatalf("lost known mapping: %+v", o)
				}
				if tc.valid {
					if !r.OK || r.CleanedCount != 2 || len(f.terminations) != 2 {
						t.Fatal(r)
					}
				} else {
					if r.OK || r.ExitCode != 3 || r.CleanedCount != 1 || o.RootDeletion != "unavailable" || len(f.terminations) != 1 || f.terminations[0] != iid(2) {
						t.Fatalf("contradictory live mapping accepted: %+v calls=%v", r, f.terminations)
					}
					requireCode(t, o, "root_volume_unverified")
				}
			})
		}
	}
}

func TestTerminalMappingAttachmentEvidence(t *testing.T) {
	for _, status := range []types.AttachmentStatus{"", "attaching", "attached", "detaching", "detached", "bogus"} {
		for _, owner := range []*string{nil, aws.String(scope.Account), aws.String("999999999999")} {
			t.Run(fmt.Sprintf("%s/owner=%s", status, aws.ToString(owner)), func(t *testing.T) {
				i := instance(1)
				i.BlockDeviceMappings[0].Ebs.Status = "attached"
				i.BlockDeviceMappings[0].Ebs.VolumeOwnerId = aws.String(scope.Account)
				f := fake(i)
				f.describe = func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					f.mu.Lock()
					current := cloneInstance(f.instances[iid(1)])
					f.mu.Unlock()
					if current.State.Name == "terminated" {
						current.BlockDeviceMappings[0].Ebs.Status = status
						current.BlockDeviceMappings[0].Ebs.VolumeOwnerId = owner
					}
					return page(current), nil
				}
				r := run(t, service(t, f), false)
				if r.TerminatedCount != 1 || len(f.terminations) != 1 {
					t.Fatal(r)
				}
				if status == "bogus" || (owner != nil && *owner != scope.Account) {
					if r.OK || r.CleanedCount != 0 || len(f.volumes) != 0 {
						t.Fatal(r)
					}
					requireCode(t, r.Instances[0], "root_volume_unverified")
				} else if !r.OK || r.CleanedCount != 1 {
					t.Fatal(r)
				}
			})
		}
	}
}

func TestDryRunDiagnosesSuppliedLiveMappingContradictions(t *testing.T) {
	for _, change := range []func(*types.EbsInstanceBlockDevice){
		func(b *types.EbsInstanceBlockDevice) { b.Status = "detached" },
		func(b *types.EbsInstanceBlockDevice) { b.Status = "bogus" },
		func(b *types.EbsInstanceBlockDevice) { b.VolumeOwnerId = aws.String("999999999999") },
	} {
		i := instance(1)
		change(i.BlockDeviceMappings[0].Ebs)
		f := fake(i, instance(2))
		r := run(t, service(t, f), true)
		if r.OK || r.ExitCode != 3 || r.CleanedCount != 0 || r.TerminatedCount != 0 || len(f.terminations) != 0 || len(f.volumes) != 0 || len(f.discovery) != 1 {
			t.Fatal(r)
		}
		requireCode(t, outcome(t, r, iid(1)), "root_volume_unverified")
		if outcome(t, r, iid(2)).Status != "would_terminate" {
			t.Fatal(r)
		}
	}
}

func TestTerminalMissingMappingsValidateSuppliedRootMetadata(t *testing.T) {
	tests := []struct {
		name   string
		change func(*types.Instance)
		valid  bool
	}{
		{"unchanged", func(i *types.Instance) {}, true},
		{"omitted type", func(i *types.Instance) { i.RootDeviceType = "" }, true},
		{"omitted name", func(i *types.Instance) { i.RootDeviceName = nil }, true},
		{"all omitted", func(i *types.Instance) { i.RootDeviceType = ""; i.RootDeviceName = nil }, true},
		{"changed type", func(i *types.Instance) { i.RootDeviceType = "instance-store" }, false},
		{"unknown type", func(i *types.Instance) { i.RootDeviceType = "bogus" }, false},
		{"changed name", func(i *types.Instance) { i.RootDeviceName = aws.String("/dev/xvdb") }, false},
	}
	for _, stage := range []string{"recheck", "observation", "historical recheck"} {
		for _, tc := range tests {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				initial := instance(1)
				if stage == "historical recheck" {
					initial = historicalInstance()
				}
				f := fake(initial)
				var sent atomic.Bool
				f.terminate = func(_ context.Context, _ *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
					sent.Store(true)
					return ack(iid(1)), nil
				}
				f.describe = func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					i := cloneInstance(initial)
					if len(in.InstanceIds) > 0 && (stage != "observation" || sent.Load()) {
						i.State = &types.InstanceState{Name: "terminated", Code: aws.Int32(48)}
						i.BlockDeviceMappings = nil
						tc.change(&i)
					}
					return page(i), nil
				}
				r := run(t, service(t, f), false)
				o := outcome(t, r, iid(1))
				wantSends := 0
				if stage == "observation" {
					wantSends = 1
				}
				if r.TerminatedCount != 1 || len(f.terminations) != wantSends {
					t.Fatal(r)
				}
				if !tc.valid {
					if r.OK || r.CleanedCount != 0 || o.RootDeletion != "unavailable" || len(f.volumes) != 0 {
						t.Fatalf("contradictory terminal metadata accepted: %+v", r)
					}
					requireCode(t, o, "root_volume_unverified")
				} else if stage == "historical recheck" {
					if !r.OK || r.CleanedCount != 0 || o.RootDeletion != "unavailable" || len(f.volumes) != 0 {
						t.Fatal(r)
					}
				} else if !r.OK || r.CleanedCount != 1 || len(f.volumes) != 1 || f.volumes[0] != vid(1) {
					t.Fatal(r)
				}
				if stage != "historical recheck" && (len(o.Volumes) != 1 || o.Volumes[0].ID != vid(1)) {
					t.Fatalf("lost captured root: %+v", o)
				}
			})
		}
	}
}

func TestDeletedVolumeAttachmentIdentityMustMatch(t *testing.T) {
	for _, tc := range []struct {
		name         string
		attachmentID *string
		valid        bool
	}{
		{"omitted", nil, true}, {"exact", aws.String(vid(1)), true}, {"foreign", aws.String(vid(99)), false}, {"empty supplied", aws.String(""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(instance(1))
			f.volume = func(_ context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
				return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(in.VolumeIds[0]), State: "deleted", Attachments: []types.VolumeAttachment{{State: "detached", VolumeId: aws.String(in.VolumeIds[0])}, {State: "detached", VolumeId: tc.attachmentID}}}}}, nil
			}
			r := run(t, service(t, f), false)
			if r.TerminatedCount != 1 || len(f.terminations) != 1 {
				t.Fatal(r)
			}
			if tc.valid {
				if !r.OK || r.CleanedCount != 1 {
					t.Fatal(r)
				}
			} else {
				if r.OK || r.CleanedCount != 0 || r.Instances[0].RootDeletion != "unavailable" {
					t.Fatalf("contradictory attachment identity accepted: %+v", r)
				}
				requireCode(t, r.Instances[0], "volume_unresolved")
			}
		})
	}
}

func TestAcknowledgementStateCodesRequireConsistentNames(t *testing.T) {
	tests := []struct {
		name              string
		previous, current *int32
		valid             bool
	}{
		{"omitted", nil, nil, true}, {"exact", aws.Int32(16), aws.Int32(32), true},
		{"high byte", aws.Int32(0xff00 | 16), aws.Int32(0x100 | 32), true},
		{"wrong current", aws.Int32(16), aws.Int32(16), false},
		{"wrong previous", aws.Int32(48), aws.Int32(32), false},
		{"wrong low byte with high bits", aws.Int32(0x100 | 16), aws.Int32(0xff00 | 16), false},
		{"negative", aws.Int32(16), aws.Int32(-224), false},
		{"beyond sixteen bits", aws.Int32(16), aws.Int32(0x10000 | 32), false},
	}
	for _, tc := range tests {
		for _, resolved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resolved=%t", tc.name, resolved), func(t *testing.T) {
				f := fake(instance(1))
				f.terminate = func(_ context.Context, _ *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
					if resolved {
						f.mu.Lock()
						i := f.instances[iid(1)]
						i.State = &types.InstanceState{Name: "terminated", Code: aws.Int32(0xff00 | 48)}
						f.instances[iid(1)] = i
						f.mu.Unlock()
					}
					a := ack(iid(1))
					a.TerminatingInstances[0].PreviousState.Code = tc.previous
					a.TerminatingInstances[0].CurrentState.Code = tc.current
					return a, nil
				}
				r := run(t, service(t, f), false)
				if len(f.terminations) != 1 {
					t.Fatal(f.terminations)
				}
				if resolved {
					if !r.OK || r.CleanedCount != 1 || r.Instances[0].Status != "termination_observed" {
						t.Fatal(r)
					}
				} else {
					want := "termination_unknown"
					if tc.valid {
						want = "termination_requested"
					}
					if r.OK || r.CleanedCount != 0 || r.Instances[0].Status != want || len(f.volumes) != 0 {
						t.Fatalf("acknowledgement status=%s want=%s: %+v", r.Instances[0].Status, want, r)
					}
				}
			})
		}
	}
}

func TestDescribeInstanceStateCodesAcrossDiscoveryRecheckAndObservation(t *testing.T) {
	for _, stage := range []string{"discovery", "recheck", "observation"} {
		for _, tc := range []struct {
			name  string
			bits  int32
			wrong bool
		}{{"high byte", 0xff00, false}, {"wrong low byte", 1, true}} {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				f := fake(instance(1))
				var dispatched atomic.Bool
				f.terminate = func(_ context.Context, _ *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
					dispatched.Store(true)
					return ack(iid(1)), nil
				}
				f.describe = func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					i := instance(1)
					if dispatched.Load() {
						i.State = &types.InstanceState{Name: "terminated", Code: aws.Int32(48)}
					}
					selected := (stage == "discovery" && len(in.InstanceIds) == 0) || (stage == "recheck" && len(in.InstanceIds) > 0 && !dispatched.Load()) || (stage == "observation" && dispatched.Load())
					if selected {
						*i.State.Code |= tc.bits
					}
					return page(i), nil
				}
				r := run(t, service(t, f), false)
				if tc.wrong {
					if r.OK || r.TerminatedCount != 0 || r.CleanedCount != 0 || len(f.volumes) != 0 {
						t.Fatal(r)
					}
					wantSends := 0
					if stage == "observation" {
						wantSends = 1
					}
					if len(f.terminations) != wantSends {
						t.Fatal(f.terminations)
					}
				} else if !r.OK || r.CleanedCount != 1 || len(f.terminations) != 1 {
					t.Fatal(r)
				}
			})
		}
	}
}
