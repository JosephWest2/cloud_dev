package expirycleanup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// Exercise the actual SDK Query serializer and retry middleware over loopback.
// These tests never load ambient credentials or contact AWS.
func TestSDKScopeSerializationAndSingleMutationAttempt(t *testing.T) {
	var mu sync.Mutex
	describeCalls, termCalls, identityCalls := 0, 0, 0
	terminated := false
	var failures []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		action := q.Get("Action")
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		if q.Get("DryRun") != "" {
			failures = append(failures, "DryRun probe")
		}
		service := "ec2"
		if action == "GetCallerIdentity" {
			service = "sts"
		}
		if !strings.Contains(r.Header.Get("Authorization"), "/us-east-2/"+service+"/aws4_request") {
			failures = append(failures, "wrong signing scope")
		}
		switch action {
		case "GetCallerIdentity":
			identityCalls++
			fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>%s</Account><Arn>arn:aws:sts::%s:assumed-role/cleanup/test</Arn><UserId>test</UserId></GetCallerIdentityResult><ResponseMetadata><RequestId>identity</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, scope.Account, scope.Account)
		case "DescribeInstances":
			describeCalls++
			expected := map[string]string{"tag:ManagedBy": "devbox", "tag:Deployment": scope.Deployment, "tag:Owner": scope.Owner}
			for n := 1; n <= 3; n++ {
				name := q.Get(fmt.Sprintf("Filter.%d.Name", n))
				value := q.Get(fmt.Sprintf("Filter.%d.Value.1", n))
				if expected[name] != value {
					failures = append(failures, "incorrect scope filter")
				}
				delete(expected, name)
			}
			if len(expected) != 0 || q.Get("Filter.4.Name") != "" {
				failures = append(failures, "missing or extra filters")
			}
			if describeCalls == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `<Response><Errors><Error><Code>RequestLimitExceeded</Code><Message>controlled throttle</Message></Error></Errors><RequestID>retry</RequestID></Response>`)
				return
			}
			if describeCalls <= 2 {
				if q.Get("InstanceId.1") != "" || q.Get("MaxResults") != "1000" {
					failures = append(failures, "wrong discovery")
				}
			} else {
				if q.Get("InstanceId.1") != iid(1) || q.Get("InstanceId.2") != "" || q.Get("MaxResults") != "" {
					failures = append(failures, "not singular exact read")
				}
			}
			state := "running"
			stateCode := 16
			if terminated {
				state = "terminated"
				stateCode = 48
			}
			fmt.Fprintf(w, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>describe</requestId><reservationSet><item><ownerId>%s</ownerId><instancesSet><item><instanceId>%s</instanceId><instanceState><code>%d</code><name>%s</name></instanceState><placement><availabilityZone>us-east-2a</availabilityZone></placement><rootDeviceType>ebs</rootDeviceType><rootDeviceName>/dev/xvda</rootDeviceName><blockDeviceMapping><item><deviceName>/dev/xvda</deviceName><ebs><volumeId>%s</volumeId><deleteOnTermination>true</deleteOnTermination></ebs></item></blockDeviceMapping><tagSet><item><key>ManagedBy</key><value>devbox</value></item><item><key>Deployment</key><value>dev</value></item><item><key>Owner</key><value>joe</value></item><item><key>ExpiresAt</key><value>%s</value></item></tagSet></item></instancesSet></item></reservationSet></DescribeInstancesResponse>`, scope.Account, iid(1), stateCode, state, vid(1), epoch.Format(time.RFC3339Nano))
		case "TerminateInstances":
			termCalls++
			if q.Get("InstanceId.1") != iid(1) || q.Get("InstanceId.2") != "" {
				failures = append(failures, "not singular mutation")
			}
			terminated = true
			// An accepted send with a lost/error response must not be retried by the
			// SDK, even when the caller supplied an aggressively retrying config.
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `<Response><Errors><Error><Code>RequestLimitExceeded</Code><Message>controlled lost response</Message></Error></Errors><RequestID>mutation</RequestID></Response>`)
		case "DescribeVolumes":
			if !terminated || q.Get("VolumeId.1") != vid(1) || q.Get("VolumeId.2") != "" {
				failures = append(failures, "untrusted volume observation")
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<Response><Errors><Error><Code>InvalidVolume.NotFound</Code><Message>controlled deletion evidence</Message></Error></Errors><RequestID>volume</RequestID></Response>`)
		default:
			failures = append(failures, "unexpected AWS action "+action)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	cfg := aws.Config{Region: scope.Region, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client(), BaseEndpoint: aws.String(server.URL), Retryer: func() aws.Retryer { return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 9 }) }}
	s, err := NewAWS(scope, cfg, clockFunc(func() time.Time { return epoch }), sinkFunc(func(context.Context, expiry.Event) error { return nil }), Limits{ReadAttempts: 2, ObservationAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := run(t, s, false)
	if !r.OK || r.CleanedCount != 1 || termCalls != 1 || describeCalls != 4 || identityCalls != 1 || len(failures) != 0 {
		t.Fatalf("result=%+v term=%d describe=%d identity=%d failures=%v", r, termCalls, describeCalls, identityCalls, failures)
	}
}
func TestSDKReadRetriesStopAtConfiguredBound(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(503)
		fmt.Fprint(w, `<Response><Errors><Error><Code>ServiceUnavailable</Code><Message>controlled</Message></Error></Errors></Response>`)
	}))
	defer server.Close()
	cfg := aws.Config{Region: scope.Region, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client(), BaseEndpoint: aws.String(server.URL)}
	s, err := NewAWS(scope, cfg, clockFunc(func() time.Time { return epoch }), sinkFunc(func(context.Context, expiry.Event) error { return nil }), Limits{ReadAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := run(t, s, false)
	if r.OK || calls != 2 || r.ScanComplete || len(r.Instances) != 0 {
		t.Fatalf("%+v calls=%d", r, calls)
	}
	cfg.Region = "us-west-2"
	if _, err := NewAWS(scope, cfg, expiry.SystemClock{}, s.deps.Sink, Limits{}); err == nil {
		t.Fatal("accepted region mismatch")
	}
}
