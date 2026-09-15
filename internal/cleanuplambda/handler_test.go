package cleanuplambda

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/JosephWest2/cloud_dev/internal/expirycleanup"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
func environment(k string) string {
	return map[string]string{"DEVBOX_ACCOUNT": "123456789012", "DEVBOX_REGION": "us-east-2", "AWS_REGION": "us-east-2", "DEVBOX_DEPLOYMENT": "test", "DEVBOX_OWNER": "owner", "DEVBOX_LOG_GROUP": "/aws/lambda/devbox-test-owner-cleanup"}[k]
}
func invocation() context.Context {
	return lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{AwsRequestID: "abcdef-1234"})
}

type journal struct {
	mu      sync.Mutex
	records []Record
	events  []expiry.Event
	fail    string
}

func (j *journal) Record(_ context.Context, r Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, r)
	if j.fail == r.Kind {
		return errors.New("secret")
	}
	return nil
}
func (j *journal) Emit(_ context.Context, e expiry.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, e)
	if j.fail == e.Kind {
		return errors.New("secret")
	}
	return nil
}

type fakeSTS struct{ account string }

func (f fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: aws.String(f.account)}, nil
}

type fakeEC2 struct {
	*ec2.Client
	instance   bool
	terminated int
	reads      int
}

func (f *fakeEC2) Options() ec2.Options { return ec2.Options{Region: "us-east-2"} }
func (f *fakeEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.reads++
	if !f.instance {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{OwnerId: aws.String("123456789012"), Instances: []ec2types.Instance{{InstanceId: aws.String("i-0123456789abcdef0"), State: &ec2types.InstanceState{Name: "running"}, RootDeviceName: aws.String("/dev/xvda"), RootDeviceType: "ebs", BlockDeviceMappings: []ec2types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/xvda"), Ebs: &ec2types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-0123456789abcdef0"), DeleteOnTermination: aws.Bool(true)}}}, Tags: []ec2types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String("test")}, {Key: aws.String("Owner"), Value: aws.String("owner")}, {Key: aws.String("ExpiresAt"), Value: aws.String("2026-09-14T11:00:00Z")}}}}}}}, nil
}
func (f *fakeEC2) TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminated++
	return nil, errors.New("secret")
}

func TestHandlerUsesSharedServiceAndReportsFailures(t *testing.T) {
	for _, tc := range []struct {
		name, account, fail string
		instance            bool
		wantWrites          int
		wantError           bool
	}{{"empty", "123456789012", "", false, 0, false}, {"wrong_identity", "999999999999", "", true, 0, true}, {"evidence_before_dispatch", "123456789012", "termination_prepared", true, 0, true}, {"termination_failure", "123456789012", "", true, 1, true}, {"start_failure", "123456789012", "invocation_start", true, 0, true}, {"end_failure", "123456789012", "invocation_end", false, 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeEC2{instance: tc.instance}
			j := &journal{fail: tc.fail}
			h := Handler{Getenv: environment, Clock: fixedClock{}, Factory: func(ctx context.Context, s Settings, id string, in expiry.ScheduledInput) (Runner, Journal, error) {
				svc, err := expirycleanup.New(s.Scope, expirycleanup.Dependencies{EC2: api, STS: fakeSTS{tc.account}, Clock: fixedClock{}, Sink: j}, expirycleanup.Limits{ObservationAttempts: 1, Concurrency: 1})
				return svc, j, err
			}}
			result, err := h.Handle(invocation(), json.RawMessage(`{"schema_version":1}`))
			if (err != nil) != tc.wantError || api.terminated != tc.wantWrites {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, api.terminated)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("raw error exposed")
			}
			if !tc.wantError && (!result.OK || result.Code != "cleanup_no_candidates" || len(j.events) != 1 || j.events[0].Kind != "summary" || len(j.records) != 2 || !j.records[1].OK) {
				t.Fatalf("empty scan must retain terminal success: %+v %+v", result, j)
			}
			if tc.name == "wrong_identity" && api.reads != 0 {
				t.Fatal("unverified identity reached EC2")
			}
		})
	}
}
func TestRejectBeforeFactory(t *testing.T) {
	bad := []string{`null`, `{}`, `{"schema_version":2}`, `{"schema_version":1,"owner":"other"}`, `{"schema_version":1,"schema_version":1}`, `{"schema_version":1,"Schema_Version":1}`, `{"schema_version":1,"attempt_number":null}`, `{"schema_version":1,"attempt_number":1}`, `{"schema_version":1} {}`, `{"schema_version":1,"execution_id":"secret\n"}`, `{"schema_version":1,"scheduled_time":"2026-09-14T00:00:00+00:00"}`}
	h := Handler{Getenv: environment, Clock: fixedClock{}, Factory: func(context.Context, Settings, string, expiry.ScheduledInput) (Runner, Journal, error) {
		t.Fatal("invalid input reached factory")
		return nil, nil, nil
	}}
	for _, raw := range bad {
		if _, err := h.Handle(invocation(), []byte(raw)); err == nil {
			t.Fatal(raw)
		}
	}
	for _, key := range []string{"DEVBOX_ACCOUNT", "DEVBOX_REGION", "AWS_REGION", "DEVBOX_DEPLOYMENT", "DEVBOX_OWNER", "DEVBOX_LOG_GROUP"} {
		h.Getenv = func(k string) string {
			if k == key {
				return ""
			}
			return environment(k)
		}
		if _, err := h.Handle(invocation(), []byte(`{"schema_version":1}`)); err == nil {
			t.Fatal(key)
		}
	}
}
func TestCorrelation(t *testing.T) {
	in, err := Decode([]byte(`{"schema_version":1,"schedule_arn":"arn:aws:scheduler:us-east-2:123456789012:schedule/group/name","scheduled_time":"2026-09-14T12:00:00Z","execution_id":"abcd-1234","attempt_number":"1"}`))
	if err != nil || in.ExecutionID != "abcd-1234" {
		t.Fatal(in, err)
	}
}

type logsFunc func(context.Context, *cloudwatchlogs.PutLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error)

func (f logsFunc) PutLogEvents(c context.Context, i *cloudwatchlogs.PutLogEventsInput, o ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error) {
	return f(c, i, o...)
}
func TestJournalRequiresAcknowledgement(t *testing.T) {
	for _, reject := range []bool{false, true} {
		j := &AWSJournal{Group: "group", Stream: "stream", Clock: fixedClock{}, Client: logsFunc(func(ctx context.Context, in *cloudwatchlogs.PutLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded journal")
			}
			if *in.LogGroupName != "group" || *in.LogStreamName != "stream" || len(in.LogEvents) != 1 {
				t.Fatal(in)
			}
			var r Record
			if json.Unmarshal([]byte(*in.LogEvents[0].Message), &r) != nil || r.Kind != "termination_prepared" || r.Event.Instance.Volumes[0].ID != "vol-01234567" {
				t.Fatal("mapping lost")
			}
			out := &cloudwatchlogs.PutLogEventsOutput{}
			if reject {
				out.RejectedLogEventsInfo = &logtypes.RejectedLogEventsInfo{TooOldLogEventEndIndex: aws.Int32(0)}
			}
			return out, nil
		})}
		err := j.Emit(context.Background(), expiry.Event{Kind: "termination_prepared", Instance: &expiry.Outcome{Volumes: []expiry.Volume{{ID: "vol-01234567"}}}})
		if (err != nil) != reject {
			t.Fatal(err)
		}
	}
}
