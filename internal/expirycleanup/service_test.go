package expirycleanup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

var scope = expiry.Scope{Account: "123456789012", Region: "us-east-2", Deployment: "dev", Owner: "joe"}
var epoch = time.Date(2026, 9, 14, 12, 0, 0, 123456789, time.UTC)

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

type sinkFunc func(context.Context, expiry.Event) error

func (f sinkFunc) Emit(ctx context.Context, e expiry.Event) error { return f(ctx, e) }

type stsFunc func(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)

func (f stsFunc) GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opts ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return f(ctx, in, opts...)
}
func apiError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: "secret provider error"}
}
func iid(n int) string { return fmt.Sprintf("i-%08x", n) }
func vid(n int) string { return fmt.Sprintf("vol-%08x", n) }
func instance(n int) types.Instance {
	return types.Instance{InstanceId: aws.String(iid(n)), State: &types.InstanceState{Name: "running", Code: aws.Int32(16)}, Placement: &types.Placement{AvailabilityZone: aws.String(scope.Region + "a")}, RootDeviceName: aws.String("/dev/xvda"), RootDeviceType: "ebs", BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/xvda"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String(vid(n)), DeleteOnTermination: aws.Bool(true)}}}, Tags: []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String(scope.Deployment)}, {Key: aws.String("Owner"), Value: aws.String(scope.Owner)}, {Key: aws.String("ExpiresAt"), Value: aws.String(epoch.Format(time.RFC3339Nano))}}}
}
func page(instances ...types.Instance) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(scope.Account), Instances: instances}}}
}
func cloneInstance(i types.Instance) types.Instance {
	b, _ := json.Marshal(i)
	var c types.Instance
	_ = json.Unmarshal(b, &c)
	return c
}

type fakeEC2 struct {
	mu           sync.Mutex
	instances    map[string]types.Instance
	discovery    []*ec2.DescribeInstancesInput
	terminations []string
	volumes      []string
	describe     func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	terminate    func(context.Context, *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error)
	volume       func(context.Context, *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	region       string
}

func fake(is ...types.Instance) *fakeEC2 {
	f := &fakeEC2{instances: map[string]types.Instance{}, region: scope.Region}
	for _, i := range is {
		f.instances[*i.InstanceId] = cloneInstance(i)
	}
	return f
}
func (f *fakeEC2) Options() ec2.Options { return ec2.Options{Region: f.region} }
func (f *fakeEC2) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.mu.Lock()
	f.discovery = append(f.discovery, in)
	f.mu.Unlock()
	if f.describe != nil {
		return f.describe(ctx, in)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	is := []types.Instance{}
	for id, i := range f.instances {
		if len(in.InstanceIds) == 0 || in.InstanceIds[0] == id {
			is = append(is, cloneInstance(i))
		}
	}
	if len(is) == 0 {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	return page(is...), nil
}
func (f *fakeEC2) TerminateInstances(ctx context.Context, in *ec2.TerminateInstancesInput, opts ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	o := ec2.Options{RetryMaxAttempts: 99}
	for _, opt := range opts {
		opt(&o)
	}
	if o.RetryMaxAttempts != 1 || o.Retryer.MaxAttempts() != 1 || in.DryRun != nil || len(in.InstanceIds) != 1 {
		panic("unsafe mutation configuration")
	}
	f.mu.Lock()
	f.terminations = append(f.terminations, in.InstanceIds[0])
	f.mu.Unlock()
	if f.terminate != nil {
		return f.terminate(ctx, in)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.instances[in.InstanceIds[0]]
	i.State = &types.InstanceState{Name: "terminated", Code: aws.Int32(48)}
	f.instances[in.InstanceIds[0]] = i
	return ack(in.InstanceIds[0]), nil
}
func ack(id string) *ec2.TerminateInstancesOutput {
	return &ec2.TerminateInstancesOutput{TerminatingInstances: []types.InstanceStateChange{{InstanceId: aws.String(id), PreviousState: &types.InstanceState{Name: "running"}, CurrentState: &types.InstanceState{Name: "shutting-down"}}}}
}
func (f *fakeEC2) DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, opts ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	f.mu.Lock()
	f.volumes = append(f.volumes, in.VolumeIds[0])
	f.mu.Unlock()
	if f.volume != nil {
		return f.volume(ctx, in)
	}
	return nil, apiError("InvalidVolume.NotFound")
}
func service(t *testing.T, f *fakeEC2, alter ...func(*Dependencies, *Limits)) *Service {
	t.Helper()
	d := Dependencies{EC2: f, STS: stsFunc(func(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
		return &sts.GetCallerIdentityOutput{Account: aws.String(scope.Account)}, nil
	}), Clock: clockFunc(func() time.Time { return epoch }), Sink: sinkFunc(func(context.Context, expiry.Event) error { return nil }), Wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
	l := Limits{ObservationAttempts: 3}
	for _, fn := range alter {
		fn(&d, &l)
	}
	s, err := New(scope, d, l)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func run(t *testing.T, s *Service, dry bool) expiry.Result {
	t.Helper()
	r, err := s.Run(context.Background(), dry)
	if (err == nil) != r.OK {
		t.Fatalf("error disagrees: %+v %v", r, err)
	}
	return r
}
func outcome(t *testing.T, r expiry.Result, id string) expiry.Outcome {
	t.Helper()
	for _, o := range r.Instances {
		if o.InstanceID == id {
			return o
		}
	}
	t.Fatalf("missing %s in %+v", id, r)
	return expiry.Outcome{}
}
func requireCode(t *testing.T, o expiry.Outcome, code string) {
	t.Helper()
	if !hasProblem(o, code) {
		t.Fatalf("missing %s: %+v", code, o)
	}
}

func TestHappyPathAndRepeat(t *testing.T) {
	f := fake(instance(1))
	events := []expiry.Event{}
	var mu sync.Mutex
	s := service(t, f, func(d *Dependencies, l *Limits) {
		d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
			return nil
		})
	})
	r := run(t, s, false)
	if !r.OK || !r.ScanComplete || !r.Complete || r.CandidateCount != 1 || r.TerminatedCount != 1 || r.CleanedCount != 1 {
		t.Fatalf("%+v", r)
	}
	if got := outcome(t, r, iid(1)); got.Status != "termination_observed" || got.RootDeletion != "deleted" {
		t.Fatal(got)
	}
	kinds := []string{}
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		if e.RunID == "" || e.Scope != scope {
			t.Fatal(e)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"decision", "termination_prepared", "outcome", "summary"}) {
		t.Fatal(kinds)
	}
	prepared := events[1].Instance
	if len(prepared.Volumes) != 1 || prepared.Volumes[0].ID != vid(1) || prepared.Volumes[0].DeleteOnTermination == nil || !*prepared.Volumes[0].DeleteOnTermination {
		t.Fatal(prepared)
	}
	r = run(t, s, false)
	if !r.OK || r.CandidateCount != 0 || r.CleanedCount != 1 || len(f.terminations) != 1 || r.Instances[0].Status != "already_terminated" {
		t.Fatalf("%+v calls=%v", r, f.terminations)
	}
	b, _ := json.Marshal(r)
	if string(b) == "" {
		t.Fatal("no result")
	}
}
func TestDryRunNoWritesOrRechecks(t *testing.T) {
	f := fake(instance(1))
	s := service(t, f, func(d *Dependencies, l *Limits) {
		d.Sink = sinkFunc(func(context.Context, expiry.Event) error { t.Error("dry-run called sink"); return nil })
	})
	r := run(t, s, true)
	if !r.OK || r.CandidateCount != 1 || r.TerminatedCount != 0 || r.CleanedCount != 0 || r.Instances[0].Status != "would_terminate" || len(f.terminations) != 0 || len(f.volumes) != 0 || len(f.discovery) != 1 {
		t.Fatal(r)
	}
	in := f.discovery[0]
	want := s.filters()
	if !reflect.DeepEqual(in.Filters, want) || len(in.InstanceIds) != 0 {
		t.Fatalf("%+v", in)
	}
}
func TestEligibilityDiagnosticsAndPeers(t *testing.T) {
	tests := []struct {
		name   string
		change func(*types.Instance)
		reason expiry.Reason
		bad    bool
	}{
		{"missing", func(i *types.Instance) { i.Tags = i.Tags[:3] }, expiry.ExpiryMissing, false},
		{"future", func(i *types.Instance) {
			i.Tags[3].Value = aws.String(epoch.Add(time.Nanosecond).Format(time.RFC3339Nano))
		}, expiry.ExpiryFuture, false},
		{"malformed", func(i *types.Instance) { i.Tags[3].Value = aws.String("bad") }, expiry.ExpiryInvalid, true},
		{"duplicate expiry", func(i *types.Instance) { i.Tags = append(i.Tags, i.Tags[3]) }, expiry.ExpiryDuplicate, true},
		{"nil expiry", func(i *types.Instance) { i.Tags[3].Value = nil }, expiry.ExpiryInvalid, true},
		{"nil key", func(i *types.Instance) { i.Tags = append(i.Tags, types.Tag{}) }, expiry.TagsInvalid, true},
		{"nil scope", func(i *types.Instance) { i.Tags[0].Value = nil }, expiry.TagsInvalid, true},
		{"duplicate scope", func(i *types.Instance) { i.Tags = append(i.Tags, i.Tags[0]) }, expiry.TagsDuplicate, true},
		{"unmanaged", func(i *types.Instance) { i.Tags[0].Value = aws.String("other") }, expiry.ScopeMismatch, true},
		{"deployment", func(i *types.Instance) { i.Tags[1].Value = aws.String("other") }, expiry.ScopeMismatch, true},
		{"owner", func(i *types.Instance) { i.Tags[2].Value = aws.String("other") }, expiry.ScopeMismatch, true},
		{"region", func(i *types.Instance) { i.Placement.AvailabilityZone = aws.String("us-east-20a") }, expiry.ScopeMismatch, true},
		{"nil state", func(i *types.Instance) { i.State = nil }, expiry.StateUnknown, true},
		{"unknown state", func(i *types.Instance) { i.State.Name = "made-up" }, expiry.StateUnknown, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			i := instance(1)
			tc.change(&i)
			f := fake(i, instance(2))
			r := run(t, service(t, f), false)
			o := outcome(t, r, iid(1))
			if o.Reason != tc.reason || o.Status != "skipped" || r.CandidateCount != 1 || r.CleanedCount != 1 || len(f.terminations) != 1 || f.terminations[0] != iid(2) {
				t.Fatalf("%+v", r)
			}
			if tc.bad {
				if r.ExitCode != 3 || r.Complete {
					t.Fatal(r)
				}
			} else if !r.OK {
				t.Fatal(r)
			}
		})
	}
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(delta.String(), func(t *testing.T) {
			f := fake(instance(1))
			s := service(t, f, func(d *Dependencies, l *Limits) { d.Clock = clockFunc(func() time.Time { return epoch.Add(delta) }) })
			r := run(t, s, true)
			want := 1
			if delta < 0 {
				want = 0
			}
			if r.CandidateCount != want {
				t.Fatal(r)
			}
		})
	}
}
func TestIncompleteDiscoveryPreservesIDsAndNeverDispatches(t *testing.T) {
	tests := []string{"error", "partial error", "nil", "cycle", "limit", "empty reservation"}
	for _, tc := range tests {
		t.Run(tc, func(t *testing.T) {
			f := fake()
			calls := 0
			f.describe = func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				calls++
				if calls == 1 {
					p := page(instance(1))
					p.NextToken = aws.String("next")
					return p, nil
				}
				switch tc {
				case "error":
					return nil, errors.New("secret")
				case "partial error":
					return page(instance(2)), errors.New("secret")
				case "nil":
					return nil, nil
				case "empty reservation":
					return page(), nil
				default:
					p := page(instance(2))
					p.NextToken = aws.String("next")
					return p, nil
				}
			}
			s := service(t, f, func(d *Dependencies, l *Limits) {
				if tc == "limit" {
					l.MaxPages = 1
				}
			})
			r := run(t, s, false)
			if r.ScanComplete || r.Complete || r.CandidateCount != 0 || r.ScannedCount < 1 || len(f.terminations) != 0 || r.ExitCode != 1 {
				t.Fatal(r)
			}
			_ = outcome(t, r, iid(1))
			if tc == "partial error" && r.ScannedCount != 2 {
				t.Fatal(r)
			}
		})
	}
}
func TestPaginationDedupeConflictAndInvalidIDs(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			f := fake(instance(1), instance(2))
			f.describe = func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				if len(in.InstanceIds) > 0 {
					f.mu.Lock()
					i := cloneInstance(f.instances[in.InstanceIds[0]])
					f.mu.Unlock()
					return page(i), nil
				}
				if in.NextToken == nil {
					p := page(instance(1))
					p.NextToken = aws.String("second")
					return p, nil
				}
				i := instance(1)
				if conflict {
					i.BlockDeviceMappings[0].Ebs.VolumeId = aws.String(vid(99))
				}
				return page(i, instance(2)), nil
			}
			r := run(t, service(t, f), false)
			if !r.ScanComplete || r.ScannedCount != 2 {
				t.Fatal(r)
			}
			if conflict {
				if r.ExitCode != 3 || r.CandidateCount != 1 || len(f.terminations) != 1 {
					t.Fatal(r)
				}
				o := outcome(t, r, iid(1))
				requireCode(t, o, "resource_invalid")
				if len(o.Volumes) != 2 {
					t.Fatal(o)
				}
			} else if !r.OK || r.CandidateCount != 2 || len(f.terminations) != 2 {
				t.Fatal(r)
			}
		})
	}
	f := fake()
	f.describe = func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		i := instance(1)
		i.InstanceId = nil
		return page(i), nil
	}
	r := run(t, service(t, f), false)
	if r.OK || !r.ScanComplete || r.ScannedCount != 0 || len(r.Instances) != 1 || len(f.terminations) != 0 {
		t.Fatal(r)
	}
}
func TestIdentityAndConstructorFailClosed(t *testing.T) {
	for _, out := range []*sts.GetCallerIdentityOutput{nil, {}, {Account: aws.String("999999999999")}, {Account: aws.String(scope.Account), Arn: aws.String("arn:aws:sts::999999999999:assumed-role/a/b")}} {
		f := fake(instance(1))
		s := service(t, f, func(d *Dependencies, l *Limits) {
			d.STS = stsFunc(func(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
				return out, nil
			})
		})
		r := run(t, s, false)
		if r.OK || r.ScanComplete || len(f.discovery) != 0 {
			t.Fatal(r)
		}
	}
	f := fake(instance(1))
	s := service(t, f)
	d := s.deps
	for _, change := range []func(*expiry.Scope, *Dependencies, *Limits){func(s *expiry.Scope, d *Dependencies, l *Limits) { s.Account = "" }, func(s *expiry.Scope, d *Dependencies, l *Limits) { s.Region = "other" }, func(s *expiry.Scope, d *Dependencies, l *Limits) { d.Sink = nil }, func(s *expiry.Scope, d *Dependencies, l *Limits) { l.Concurrency = 5 }, func(s *expiry.Scope, d *Dependencies, l *Limits) { l.RequestTimeout = 16 * time.Second }, func(s *expiry.Scope, d *Dependencies, l *Limits) { l.InvocationTimeout = 166 * time.Second }, func(s *expiry.Scope, d *Dependencies, l *Limits) { l.MaxPages = 129 }, func(s *expiry.Scope, d *Dependencies, l *Limits) { l.ObservationAttempts = 31 }} {
		sc, dp, l := scope, d, Limits{}
		change(&sc, &dp, &l)
		if _, err := New(sc, dp, l); err == nil {
			t.Fatal("accepted invalid constructor")
		}
	}
}

func TestFinalRecheckRejectsForgedAndDriftedEvidence(t *testing.T) {
	tests := []struct {
		name     string
		response func() (*ec2.DescribeInstancesOutput, error)
		code     string
	}{
		{"nil", func() (*ec2.DescribeInstancesOutput, error) { return nil, nil }, "resource_unverified"},
		{"missing", func() (*ec2.DescribeInstancesOutput, error) { return &ec2.DescribeInstancesOutput{}, nil }, "resource_unverified"},
		{"foreign", func() (*ec2.DescribeInstancesOutput, error) { return page(instance(9)), nil }, "resource_unverified"},
		{"extra", func() (*ec2.DescribeInstancesOutput, error) { return page(instance(1), instance(9)), nil }, "resource_unverified"},
		{"duplicate", func() (*ec2.DescribeInstancesOutput, error) { return page(instance(1), instance(1)), nil }, "resource_unverified"},
		{"partial", func() (*ec2.DescribeInstancesOutput, error) { return page(instance(1)), errors.New("lost") }, "resource_unverified"},
		{"cycle", func() (*ec2.DescribeInstancesOutput, error) {
			p := page(instance(1))
			p.NextToken = aws.String("cycle")
			return p, nil
		}, "resource_unverified"},
		{"account", func() (*ec2.DescribeInstancesOutput, error) {
			p := page(instance(1))
			p.Reservations[0].OwnerId = aws.String("999999999999")
			return p, nil
		}, "scope_mismatch"},
		{"scope", func() (*ec2.DescribeInstancesOutput, error) {
			i := instance(1)
			i.Tags[2].Value = aws.String("other")
			return page(i), nil
		}, "scope_mismatch"},
		{"removed", func() (*ec2.DescribeInstancesOutput, error) {
			i := instance(1)
			i.Tags = i.Tags[:3]
			return page(i), nil
		}, ""},
		{"changed past", func() (*ec2.DescribeInstancesOutput, error) {
			i := instance(1)
			i.Tags[3].Value = aws.String(epoch.Add(-time.Hour).Format(time.RFC3339Nano))
			return page(i), nil
		}, ""},
		{"future", func() (*ec2.DescribeInstancesOutput, error) {
			i := instance(1)
			i.Tags[3].Value = aws.String(epoch.Add(time.Hour).Format(time.RFC3339Nano))
			return page(i), nil
		}, ""},
		{"root changed", func() (*ec2.DescribeInstancesOutput, error) {
			i := instance(1)
			i.BlockDeviceMappings[0].Ebs.VolumeId = aws.String(vid(9))
			return page(i), nil
		}, "root_volume_unverified"},
		{"flag changed", func() (*ec2.DescribeInstancesOutput, error) {
			i := instance(1)
			i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
			return page(i), nil
		}, "root_volume_unverified"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fake()
			f.describe = func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				if len(in.InstanceIds) == 0 {
					return page(instance(1)), nil
				}
				return tc.response()
			}
			r := run(t, service(t, f), false)
			if r.CandidateCount != 1 || len(f.terminations) != 0 || outcome(t, r, iid(1)).InstanceID != iid(1) {
				t.Fatal(r)
			}
			if tc.code != "" {
				requireCode(t, r.Instances[0], tc.code)
			} else if !r.OK {
				t.Fatal(r)
			}
		})
	}
}
func TestSinkSnapshotsAndClockAfterAcknowledgement(t *testing.T) {
	f := fake(instance(1))
	var backwards atomic.Bool
	s := service(t, f, func(d *Dependencies, l *Limits) {
		d.Clock = clockFunc(func() time.Time {
			if backwards.Load() {
				return epoch.Add(-time.Nanosecond)
			}
			return epoch
		})
		d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
			if e.Instance != nil {
				e.Instance.InstanceID = iid(99)
				*e.Instance.ExpiresAt = "forged"
				*e.Instance.Volumes[0].DeleteOnTermination = false
				e.Instance.Volumes[0].ID = vid(99)
			}
			if e.Kind == "termination_prepared" {
				backwards.Store(true)
			}
			if e.Summary != nil {
				e.Summary.Instances[0].InstanceID = iid(99)
			}
			return nil
		})
	})
	r := run(t, s, false)
	if len(f.terminations) != 0 || r.Instances[0].InstanceID != iid(1) || r.Instances[0].Reason != expiry.ExpiryFuture || *r.Instances[0].ExpiresAt != epoch.Format(time.RFC3339Nano) || !*r.Instances[0].Volumes[0].DeleteOnTermination || r.Instances[0].Volumes[0].ID != vid(1) {
		t.Fatal(r)
	}
}
func TestRootMappingRequirements(t *testing.T) {
	for _, tc := range []string{"missing", "nil flag", "retained", "duplicate device", "duplicate volume", "nil EBS", "bad volume", "root name", "root type"} {
		t.Run(tc, func(t *testing.T) {
			i := instance(1)
			switch tc {
			case "missing":
				i.BlockDeviceMappings = nil
			case "nil flag":
				i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = nil
			case "retained":
				i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
			case "duplicate device", "duplicate volume":
				b := i.BlockDeviceMappings[0]
				if tc == "duplicate volume" {
					b.DeviceName = aws.String("/dev/xvdb")
				}
				i.BlockDeviceMappings = append(i.BlockDeviceMappings, b)
			case "nil EBS":
				i.BlockDeviceMappings[0].Ebs = nil
			case "bad volume":
				i.BlockDeviceMappings[0].Ebs.VolumeId = aws.String("bad")
			case "root name":
				i.RootDeviceName = nil
			case "root type":
				i.RootDeviceType = "instance-store"
			}
			f := fake(i, instance(2))
			r := run(t, service(t, f), false)
			if r.ExitCode != 3 || r.CandidateCount != 2 || r.CleanedCount != 1 || len(f.terminations) != 1 || f.terminations[0] != iid(2) {
				t.Fatal(r)
			}
			code := "root_volume_unverified"
			if tc == "retained" {
				code = "root_volume_retained"
			}
			requireCode(t, outcome(t, r, iid(1)), code)
		})
	}
}

func TestDenialProtectionThrottlingAndLaterScanRecovery(t *testing.T) {
	for _, code := range []string{"UnauthorizedOperation", "OperationNotPermitted", "RequestLimitExceeded"} {
		t.Run(code, func(t *testing.T) {
			f := fake(instance(1), instance(2))
			var recovered atomic.Bool
			f.terminate = func(ctx context.Context, in *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
				id := in.InstanceIds[0]
				if id == iid(1) && !recovered.Load() {
					return nil, apiError(code)
				}
				f.mu.Lock()
				i := f.instances[id]
				i.State = &types.InstanceState{Name: "terminated"}
				f.instances[id] = i
				f.mu.Unlock()
				return ack(id), nil
			}
			s := service(t, f)
			r := run(t, s, false)
			if r.ExitCode != 3 || r.CleanedCount != 1 || len(f.terminations) != 2 {
				t.Fatal(r)
			}
			o := outcome(t, r, iid(1))
			want := "termination_denied"
			if code == "OperationNotPermitted" {
				want = "termination_protected"
			}
			if code == "RequestLimitExceeded" {
				want = "termination_unresolved"
				if o.Status != "termination_unknown" {
					t.Fatal(o)
				}
			}
			requireCode(t, o, want)
			recovered.Store(true)
			r = run(t, s, false)
			if !r.OK || r.CleanedCount != 2 || len(f.terminations) != 3 {
				t.Fatalf("later retry: %+v calls=%v", r, f.terminations)
			}
		})
	}
}
func TestUnknownAcknowledgementResolvedOnlyByExactObservation(t *testing.T) {
	for _, tc := range []string{"lost", "nil", "empty", "foreign", "duplicate", "partial", "missing instance"} {
		t.Run(tc, func(t *testing.T) {
			f := fake(instance(1))
			f.terminate = func(ctx context.Context, in *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
				f.mu.Lock()
				defer f.mu.Unlock()
				i := f.instances[iid(1)]
				i.State = &types.InstanceState{Name: "terminated"}
				i.BlockDeviceMappings = nil
				f.instances[iid(1)] = i
				switch tc {
				case "lost":
					return nil, errors.New("lost response")
				case "nil":
					return nil, nil
				case "empty":
					return &ec2.TerminateInstancesOutput{}, nil
				case "foreign":
					return ack(iid(9)), nil
				case "duplicate":
					a := ack(iid(1))
					a.TerminatingInstances = append(a.TerminatingInstances, a.TerminatingInstances[0])
					return a, nil
				case "partial":
					return ack(iid(1)), errors.New("partial")
				default:
					delete(f.instances, iid(1))
					return ack(iid(1)), nil
				}
			}
			r := run(t, service(t, f), false)
			if len(f.terminations) != 1 {
				t.Fatal(f.terminations)
			}
			if tc == "missing instance" {
				if r.OK || r.TerminatedCount != 0 || r.CleanedCount != 0 || len(f.volumes) != 0 {
					t.Fatal(r)
				}
				requireCode(t, r.Instances[0], "termination_unresolved")
			} else if !r.OK || r.CleanedCount != 1 || r.TerminatedCount != 1 || r.Instances[0].Volumes[0].ID != vid(1) {
				t.Fatal(r)
			}
		})
	}
}
func TestVolumeEvidenceIsExactAndIndependent(t *testing.T) {
	tests := []struct {
		name     string
		response func(string) (*ec2.DescribeVolumesOutput, error)
		deleted  bool
	}{
		{"notfound", func(id string) (*ec2.DescribeVolumesOutput, error) { return nil, apiError("InvalidVolume.NotFound") }, true},
		{"deleted", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(id), State: "deleted"}}}, nil
		}, true},
		{"nil", func(id string) (*ec2.DescribeVolumesOutput, error) { return nil, nil }, false},
		{"empty", func(id string) (*ec2.DescribeVolumesOutput, error) { return &ec2.DescribeVolumesOutput{}, nil }, false},
		{"contradictory notfound", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{}, apiError("InvalidVolume.NotFound")
		}, false},
		{"wrong ID", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(vid(99)), State: "deleted"}}}, nil
		}, false},
		{"duplicate", func(id string) (*ec2.DescribeVolumesOutput, error) {
			v := types.Volume{VolumeId: aws.String(id), State: "deleted"}
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{v, v}}, nil
		}, false},
		{"paged", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{NextToken: aws.String("next"), Volumes: []types.Volume{{VolumeId: aws.String(id), State: "deleted"}}}, nil
		}, false},
		{"attached", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(id), State: "deleted", Attachments: []types.VolumeAttachment{{State: "attached"}}}}}, nil
		}, false},
		{"wrong account", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(id), State: "deleted", OwnerId: aws.String("999999999999")}}}, nil
		}, false},
		{"wrong zone", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(id), State: "deleted", AvailabilityZone: aws.String("us-east-20a")}}}, nil
		}, false},
		{"wrong ARN", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(id), State: "deleted", VolumeArn: aws.String("arn:aws-cn:ec2:us-east-2:123456789012:volume/" + id)}}}, nil
		}, false},
		{"still available", func(id string) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String(id), State: "available"}}}, nil
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(instance(1))
			f.volume = func(ctx context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
				if len(in.VolumeIds) != 1 || in.VolumeIds[0] != vid(1) {
					t.Errorf("unsafe volume query %+v", in)
				}
				return tc.response(in.VolumeIds[0])
			}
			r := run(t, service(t, f), false)
			if r.TerminatedCount != 1 || len(f.terminations) != 1 || r.OK != tc.deleted {
				t.Fatal(r)
			}
			if tc.deleted {
				if r.CleanedCount != 1 {
					t.Fatal(r)
				}
			} else {
				if r.CleanedCount != 0 || r.Instances[0].RootDeletion != "unavailable" {
					t.Fatal(r)
				}
				requireCode(t, r.Instances[0], "volume_unresolved")
			}
		})
	}
	t.Run("independent nonroot", func(t *testing.T) {
		i := instance(1)
		i.BlockDeviceMappings = append(i.BlockDeviceMappings, types.InstanceBlockDeviceMapping{DeviceName: aws.String("/dev/xvdb"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String(vid(2)), DeleteOnTermination: aws.Bool(true)}})
		f := fake(i)
		f.volume = func(ctx context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
			if in.VolumeIds[0] == vid(2) {
				return nil, apiError("AccessDenied")
			}
			return nil, apiError("InvalidVolume.NotFound")
		}
		r := run(t, service(t, f), false)
		if r.OK || r.CleanedCount != 1 || r.TerminatedCount != 1 || r.Instances[0].RootDeletion != "deleted" || len(r.Instances[0].Errors) == 0 {
			t.Fatal(r)
		}
	})
}
func TestTerminalAndNoCandidateOutcomes(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		r := run(t, service(t, fake()), false)
		if !r.OK || r.Code != "cleanup_no_candidates" || r.Instances == nil || r.Errors == nil {
			t.Fatal(r)
		}
	})
	t.Run("malformed only", func(t *testing.T) {
		i := instance(1)
		i.Tags[3].Value = nil
		r := run(t, service(t, fake(i)), false)
		if r.OK || r.ExitCode != 1 || !r.ScanComplete || r.CandidateCount != 0 {
			t.Fatal(r)
		}
	})
	for _, state := range []string{"shutting-down", "terminated"} {
		for _, root := range []string{"known", "missing", "retained"} {
			t.Run(state+root, func(t *testing.T) {
				i := instance(1)
				i.State = &types.InstanceState{Name: types.InstanceStateName(state)}
				if root == "missing" {
					i.BlockDeviceMappings = nil
				}
				if root == "retained" {
					i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
				}
				f := fake(i)
				if state == "shutting-down" {
					var exact atomic.Int32
					f.describe = func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
						cur := cloneInstance(i)
						if len(in.InstanceIds) > 0 && exact.Add(1) > 1 {
							cur.State = &types.InstanceState{Name: "terminated"}
						}
						return page(cur), nil
					}
				}
				r := run(t, service(t, f), false)
				if r.CandidateCount != 0 || r.TerminatedCount != 1 || len(f.terminations) != 0 {
					t.Fatal(r)
				}
				if root == "known" {
					if !r.OK || r.CleanedCount != 1 {
						t.Fatal(r)
					}
				} else if r.OK || r.CleanedCount != 0 || len(f.volumes) != 0 {
					t.Fatal(r)
				}
			})
		}
	}
}
func TestSinkFailureIsolationAndCancellation(t *testing.T) {
	for _, kind := range []string{"decision", "termination_prepared", "outcome", "summary"} {
		t.Run(kind, func(t *testing.T) {
			f := fake(instance(1), instance(2))
			s := service(t, f, func(d *Dependencies, l *Limits) {
				d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
					if e.Kind == kind && (e.Instance == nil || e.Instance.InstanceID == iid(1)) {
						return errors.New("secret sink error")
					}
					return nil
				})
			})
			r := run(t, s, false)
			want := 1
			if kind == "outcome" || kind == "summary" {
				want = 2
			}
			if r.OK || r.ExitCode != 3 || r.CleanedCount != want || len(f.terminations) != want {
				t.Fatal(r)
			}
		})
	}
	t.Run("slow context sink", func(t *testing.T) {
		f := fake(instance(1), instance(2), instance(3), instance(4), instance(5))
		s := service(t, f, func(d *Dependencies, l *Limits) {
			l.RequestTimeout = 10 * time.Millisecond
			d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
				if e.Kind == "termination_prepared" && e.Instance.InstanceID == iid(1) {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			})
		})
		r := run(t, s, false)
		if r.ExitCode != 3 || r.CleanedCount != 4 || len(f.terminations) != 4 {
			t.Fatal(r)
		}
		requireCode(t, outcome(t, r, iid(1)), "evidence_unavailable")
	})
	t.Run("invocation canceled with queued IDs", func(t *testing.T) {
		is := []types.Instance{}
		for i := 1; i <= 9; i++ {
			is = append(is, instance(i))
		}
		f := fake(is...)
		s := service(t, f, func(d *Dependencies, l *Limits) {
			l.InvocationTimeout = 10 * time.Millisecond
			l.Concurrency = 1
			d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
				if e.Kind == "termination_prepared" {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			})
		})
		r := run(t, s, false)
		if r.ExitCode != 4 || r.CandidateCount != 9 || r.ScannedCount != 9 || len(r.Instances) != 9 || len(f.terminations) != 0 {
			t.Fatal(r)
		}
	})
	t.Run("canceled after dispatch", func(t *testing.T) {
		f := fake(instance(1), instance(2))
		ctx, cancel := context.WithCancel(context.Background())
		f.terminate = func(ctx context.Context, in *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
			cancel()
			return nil, ctx.Err()
		}
		r, err := service(t, f).Run(ctx, false)
		if err == nil || r.ExitCode != 4 || len(r.Instances) != 2 || r.CandidateCount != 2 || r.CleanedCount != 0 {
			t.Fatal(r)
		}
	})
}
func TestBoundedConcurrentRequestsAndSlowPeer(t *testing.T) {
	is := []types.Instance{}
	for i := 1; i <= 12; i++ {
		is = append(is, instance(i))
	}
	f := fake(is...)
	var active, peak atomic.Int32
	f.terminate = func(ctx context.Context, in *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		if in.InstanceIds[0] == iid(1) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		time.Sleep(time.Millisecond)
		f.mu.Lock()
		i := f.instances[in.InstanceIds[0]]
		i.State = &types.InstanceState{Name: "terminated"}
		f.instances[in.InstanceIds[0]] = i
		f.mu.Unlock()
		return ack(in.InstanceIds[0]), nil
	}
	s := service(t, f, func(d *Dependencies, l *Limits) { l.RequestTimeout = 10 * time.Millisecond })
	r := run(t, s, false)
	if r.ExitCode != 3 || r.CleanedCount != 11 || len(f.terminations) != 12 || peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("%+v peak=%d", r, peak.Load())
	}
}
func TestTrulyConcurrentRunsHaveIndependentAuthority(t *testing.T) {
	f := fake(instance(1))
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var prepared atomic.Int32
	s := service(t, f, func(d *Dependencies, l *Limits) {
		d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
			if e.Kind == "termination_prepared" {
				prepared.Add(1)
				arrived <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	})
	results := make(chan expiry.Result, 2)
	for i := 0; i < 2; i++ {
		go func() { r, _ := s.Run(context.Background(), false); results <- r }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("runs did not overlap")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		r := <-results
		if !r.OK || r.CandidateCount != 1 || r.CleanedCount != 1 {
			t.Fatal(r)
		}
	}
	if prepared.Load() != 2 || len(f.terminations) < 1 || len(f.terminations) > 2 {
		t.Fatal(f.terminations)
	}
}
func TestRetainedEventSnapshotsAreIndependentOfReturnedResults(t *testing.T) {
	f := fake(instance(1))
	var events []expiry.Event
	var mu sync.Mutex
	s := service(t, f, func(d *Dependencies, l *Limits) {
		d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
			return nil
		})
	})
	r := run(t, s, false)
	r.Instances[0].Volumes[0].ID = vid(99)
	*r.Instances[0].ExpiresAt = "forged"
	for _, e := range events {
		if e.Instance != nil && (e.Instance.Volumes[0].ID != vid(1) || *e.Instance.ExpiresAt == "forged") {
			t.Fatal(e)
		}
		if e.Summary != nil && e.Summary.Instances[0].Volumes[0].ID != vid(1) {
			t.Fatal(e)
		}
	}
}

func TestPartialIdentityAndRegionEvidenceSkipsOnlyInvalidPeers(t *testing.T) {
	for _, owner := range []*string{nil, aws.String("999999999999")} {
		f := fake(instance(2))
		f.describe = func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			if len(in.InstanceIds) == 0 {
				p := page(instance(1))
				p.Reservations[0].OwnerId = owner
				p.Reservations = append(p.Reservations, page(instance(2)).Reservations...)
				return p, nil
			}
			f.mu.Lock()
			i := cloneInstance(f.instances[iid(2)])
			f.mu.Unlock()
			return page(i), nil
		}
		r := run(t, service(t, f), false)
		if r.ExitCode != 3 || r.CandidateCount != 1 || r.CleanedCount != 1 || len(f.terminations) != 1 {
			t.Fatal(r)
		}
		requireCode(t, outcome(t, r, iid(1)), "scope_mismatch")
	}
}
func TestTypedNilDependenciesAreInvalid(t *testing.T) {
	s := service(t, fake())
	for _, change := range []func(*Dependencies){func(d *Dependencies) { d.EC2 = (*fakeEC2)(nil) }, func(d *Dependencies) { d.STS = stsFunc(nil) }, func(d *Dependencies) { d.Clock = clockFunc(nil) }, func(d *Dependencies) { d.Sink = sinkFunc(nil) }} {
		d := s.deps
		change(&d)
		if _, err := New(scope, d, Limits{}); err == nil {
			t.Fatal("accepted typed nil dependency")
		}
	}
}
func TestObservationRecoversAfterReadFailureAndMappingDriftCannotProveRootDeletion(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprint(drift), func(t *testing.T) {
			f := fake(instance(1))
			var dispatched atomic.Bool
			var observes atomic.Int32
			f.terminate = func(ctx context.Context, in *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
				dispatched.Store(true)
				return nil, errors.New("lost")
			}
			f.describe = func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				i := instance(1)
				if dispatched.Load() {
					if observes.Add(1) == 1 {
						return nil, apiError("RequestLimitExceeded")
					}
					i.State = &types.InstanceState{Name: "terminated"}
					if drift {
						i.BlockDeviceMappings[0].Ebs.VolumeId = aws.String(vid(99))
					} else {
						i.BlockDeviceMappings = nil
					}
				}
				return page(i), nil
			}
			r := run(t, service(t, f), false)
			if r.TerminatedCount != 1 || len(f.terminations) != 1 {
				t.Fatal(r)
			}
			if drift {
				if r.OK || r.CleanedCount != 0 || len(f.volumes) != 0 || len(r.Instances[0].Volumes) != 2 {
					t.Fatal(r)
				}
				requireCode(t, r.Instances[0], "root_volume_unverified")
			} else if !r.OK || r.CleanedCount != 1 {
				t.Fatal(r)
			}
		})
	}
}
func TestScanCancellationRetainsPartialPageAndZeroAuthority(t *testing.T) {
	f := fake()
	ctx, cancel := context.WithCancel(context.Background())
	f.describe = func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		cancel()
		return page(instance(1)), context.Canceled
	}
	r, err := service(t, f).Run(ctx, false)
	if err == nil || r.ExitCode != 4 || r.ScanComplete || r.CandidateCount != 0 || r.ScannedCount != 1 || len(r.Instances) != 1 || len(f.terminations) != 0 {
		t.Fatal(r)
	}
}
func TestFrozenDiscoveryDoesNotAddNewlyExpiredWorkers(t *testing.T) {
	i := instance(1)
	i.Tags[3].Value = aws.String(epoch.Add(time.Nanosecond).Format(time.RFC3339Nano))
	f := fake(i, instance(2))
	var advance atomic.Bool
	s := service(t, f, func(d *Dependencies, l *Limits) {
		d.Clock = clockFunc(func() time.Time {
			if advance.Load() {
				return epoch.Add(time.Hour)
			}
			return epoch
		})
		d.Sink = sinkFunc(func(context.Context, expiry.Event) error { advance.Store(true); return nil })
	})
	r := run(t, s, false)
	if !r.OK || r.CandidateCount != 1 || len(f.terminations) != 1 || f.terminations[0] != iid(2) || outcome(t, r, iid(1)).Reason != expiry.ExpiryFuture {
		t.Fatal(r)
	}
}

func TestExactRecheckPaginationAndStateContradiction(t *testing.T) {
	for _, contradiction := range []bool{false, true} {
		t.Run(fmt.Sprint(contradiction), func(t *testing.T) {
			f := fake(instance(1))
			f.describe = func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				if len(in.InstanceIds) == 0 {
					return page(instance(1)), nil
				}
				if in.NextToken == nil {
					return &ec2.DescribeInstancesOutput{NextToken: aws.String("exact-second")}, nil
				}
				f.mu.Lock()
				i := cloneInstance(f.instances[iid(1)])
				f.mu.Unlock()
				if contradiction {
					i.State = &types.InstanceState{Name: "running", Code: aws.Int32(48)}
				}
				return page(i), nil
			}
			r := run(t, service(t, f), false)
			if contradiction {
				if r.OK || len(f.terminations) != 0 {
					t.Fatal(r)
				}
				requireCode(t, r.Instances[0], "state_unknown")
			} else if !r.OK || r.CleanedCount != 1 || len(f.terminations) != 1 {
				t.Fatal(r)
			}
		})
	}
}
func TestSlowObservationDoesNotSuppressPeersOrOtherVolumes(t *testing.T) {
	i := instance(1)
	i.BlockDeviceMappings = append(i.BlockDeviceMappings, types.InstanceBlockDeviceMapping{DeviceName: aws.String("/dev/xvdb"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String(vid(99)), DeleteOnTermination: aws.Bool(true)}})
	f := fake(i, instance(2), instance(3), instance(4), instance(5))
	f.volume = func(ctx context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
		if in.VolumeIds[0] == vid(1) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, apiError("InvalidVolume.NotFound")
	}
	r := run(t, service(t, f, func(d *Dependencies, l *Limits) { l.RequestTimeout = 20 * time.Millisecond }), false)
	if r.ExitCode != 3 || r.TerminatedCount != 5 || r.CleanedCount != 4 {
		t.Fatal(r)
	}
	o := outcome(t, r, iid(1))
	if o.Volumes[1].Deletion != "deleted" {
		t.Fatal(o)
	}
}
func TestSinkMayRetainAndMutateItsOwnedEventsConcurrently(t *testing.T) {
	f := fake(instance(1))
	var wg sync.WaitGroup
	done := make(chan struct{})
	s := service(t, f, func(d *Dependencies, l *Limits) {
		d.Sink = sinkFunc(func(ctx context.Context, e expiry.Event) error {
			if e.Instance != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-done:
							return
						default:
							e.Instance.InstanceID = iid(99)
							e.Instance.Volumes[0].ID = vid(99)
							*e.Instance.Volumes[0].DeleteOnTermination = false
							*e.Instance.ExpiresAt = "forged"
						}
					}
				}()
			}
			return nil
		})
	})
	r := run(t, s, false)
	close(done)
	wg.Wait()
	if !r.OK || r.CleanedCount != 1 || r.Instances[0].InstanceID != iid(1) || r.Instances[0].Volumes[0].ID != vid(1) || len(f.terminations) != 1 || f.terminations[0] != iid(1) {
		t.Fatal(r)
	}
}
