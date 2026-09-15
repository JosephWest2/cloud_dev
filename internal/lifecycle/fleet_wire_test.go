package lifecycle

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

const fleetWireID = "fleet-01234567-89ab-cdef-0123-456789abcdef"

func fleetWireFixture(t *testing.T, onDemand bool) (LaunchPlan, AttemptReceipt) {
	t.Helper()
	c, m, p, selection := batchFixture(t)
	// The effective disk deliberately differs from the template's 100 GiB default.
	p.DiskGB = 137
	image := m.Images["agent"]
	image.LaunchTemplateVersion = "7"
	m.Images["agent"] = image
	selection.OnDemand = onDemand
	plan, err := BuildLaunchPlan(c, m, p, selection, strings.Repeat("a", 32), "2026-09-14T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	attemptID, err := AttemptID(plan.RequestID, "")
	if err != nil {
		t.Fatal(err)
	}
	attempt := AttemptReceipt{
		AttemptID: attemptID, ClientToken: attemptToken(plan.Digest(), attemptID, 2),
		RequestedCount: 2, CreatedAt: plan.CreatedAt, State: "prepared",
	}
	return plan, attempt
}

func fleetWireClient(server *httptest.Server, maxAttempts int) *ec2.Client {
	return ec2.NewFromConfig(aws.Config{
		Region: "us-east-2", Credentials: aws.AnonymousCredentials{},
		BaseEndpoint: aws.String(server.URL), HTTPClient: server.Client(),
		Retryer: func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = maxAttempts
				o.MaxBackoff = time.Nanosecond
			})
		},
	})
}

func fleetWireSuccess(market string) string {
	return `<CreateFleetResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">` +
		`<requestId>test-request</requestId><fleetId>` + fleetWireID + `</fleetId><errorSet/>` +
		`<fleetInstanceSet><item><instanceIds><item>i-0123456789abcdef0</item><item>i-0123456789abcdef1</item></instanceIds>` +
		`<instanceType>c7i.2xlarge</instanceType><lifecycle>` + market + `</lifecycle>` +
		`<availabilityZone>us-east-2a</availabilityZone><subnetId>subnet-12345678</subnetId>` +
		`</item></fleetInstanceSet></CreateFleetResponse>`
}

// Assert the actual EC2 Query protocol, not only Go fields. An exact parameter
// set also catches accidental maintenance, fallback, weights, or floating pins.
func TestFleetSDKSerializedMarketAndOverrides(t *testing.T) {
	for _, market := range []string{"spot", "on-demand"} {
		t.Run(market, func(t *testing.T) {
			plan, attempt := fleetWireFixture(t, market == "on-demand")
			input, err := BuildFleetInput(plan, attempt)
			if err != nil {
				t.Fatal(err)
			}
			requests := make(chan url.Values, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- r.PostForm
				w.Header().Set("Content-Type", "text/xml")
				_, _ = io.WriteString(w, fleetWireSuccess(market))
			}))
			defer server.Close()
			out, err := fleetWireClient(server, 1).CreateFleet(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if aws.ToString(out.FleetId) != fleetWireID || len(out.Instances) != 1 || !reflect.DeepEqual(out.Instances[0].InstanceIds, []string{"i-0123456789abcdef0", "i-0123456789abcdef1"}) || out.Errors == nil || len(out.Errors) != 0 {
				t.Fatalf("unexpected decoded instant Fleet result: %+v", out)
			}
			var got url.Values
			select {
			case got = <-requests:
			default:
				t.Fatal("CreateFleet did not reach the local server")
			}
			want := url.Values{
				"Action": {"CreateFleet"}, "Version": {"2016-11-15"}, "Type": {"instant"},
				"ClientToken": {attempt.ClientToken},
				"TargetCapacitySpecification.TotalTargetCapacity":                      {"2"},
				"TargetCapacitySpecification.DefaultTargetCapacityType":                {market},
				"LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateId": {"lt-12345678"},
				"LaunchTemplateConfigs.1.LaunchTemplateSpecification.Version":          {"7"},
			}
			choices := [][3]string{
				{"c6i.2xlarge", "subnet-87654321", "us-east-2b"},
				{"c7i.2xlarge", "subnet-12345678", "us-east-2a"},
				{"c7i.2xlarge", "subnet-87654321", "us-east-2b"},
			}
			if market == "spot" {
				want.Set("SpotOptions.AllocationStrategy", "price-capacity-optimized")
				want.Set("TargetCapacitySpecification.SpotTargetCapacity", "2")
				want.Set("TargetCapacitySpecification.OnDemandTargetCapacity", "0")
			} else {
				want.Set("OnDemandOptions.AllocationStrategy", "lowest-price")
				want.Set("TargetCapacitySpecification.SpotTargetCapacity", "0")
				want.Set("TargetCapacitySpecification.OnDemandTargetCapacity", "2")
				// The profile's first type is c7i, although c6i sorts first.
				choices = choices[1:]
			}
			for i, choice := range choices {
				prefix := "LaunchTemplateConfigs.1.Overrides." + strconv.Itoa(i+1) + "."
				for key, value := range map[string]string{
					"InstanceType": choice[0], "SubnetId": choice[1], "AvailabilityZone": choice[2],
					"ImageId": "ami-12345678", "MetadataOptions.HttpTokens": "required",
					"BlockDeviceMapping.1.DeviceName":     "/dev/sda1",
					"BlockDeviceMapping.1.Ebs.VolumeSize": "137", "BlockDeviceMapping.1.Ebs.VolumeType": "gp3",
					"BlockDeviceMapping.1.Ebs.Encrypted": "true", "BlockDeviceMapping.1.Ebs.DeleteOnTermination": "true",
				} {
					want.Set(prefix+key, value)
				}
			}
			// These metadata keys are deliberately independent of AttemptTags. Every
			// resource receives every tag at creation in lexical key order.
			tags := [][2]string{
				{"AttemptId", attempt.AttemptID}, {"BaseName", "smoke-batch"}, {"BatchId", strings.Repeat("a", 32)},
				{"CreatedAt", "2026-09-14T00:00:00Z"}, {"Deployment", "test"}, {"ExpiresAt", "2026-09-14T02:00:00Z"}, {"Group", "smoke-batch"},
				{"ManagedBy", "devbox"}, {"Name", "smoke-batch"}, {"NamingVersion", "1"},
				{"Owner", "test-owner"}, {"Profile", "agent"}, {"RequestId", strings.Repeat("a", 32)},
			}
			for i, resource := range []string{"fleet", "instance", "volume"} {
				prefix := "TagSpecification." + strconv.Itoa(i+1) + "."
				want.Set(prefix+"ResourceType", resource)
				for j, tag := range tags {
					tagPrefix := prefix + "Tag." + strconv.Itoa(j+1) + "."
					want.Set(tagPrefix+"Key", tag[0])
					want.Set(tagPrefix+"Value", tag[1])
				}
			}
			if !reflect.DeepEqual(got, want) {
				for key, value := range want {
					if !reflect.DeepEqual(got[key], value) {
						t.Errorf("serialized %s = %q; want %q", key, got[key], value)
					}
				}
				for key, value := range got {
					if !want.Has(key) {
						t.Errorf("unexpected serialized parameter %s = %q", key, value)
					}
				}
			}
		})
	}
}

// A lost response does not authorize another invocation. This test exercises
// only the bounded transport retries inside one SDK call and proves that their
// serialized token and complete parameters are identical. AWS's actual token
// retention and allocation semantics require the separate live acceptance gate.
func TestFleetSDKRetriesKeepExactDispatch(t *testing.T) {
	for _, failure := range []string{"throttled", "accepted-response-lost"} {
		t.Run(failure, func(t *testing.T) {
			plan, attempt := fleetWireFixture(t, false)
			input, err := BuildFleetInput(plan, attempt)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				bodies = append(bodies, string(body))
				call := len(bodies)
				mu.Unlock()
				if call == 1 {
					if failure == "throttled" {
						w.Header().Set("Content-Type", "text/xml")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `<Response><Errors><Error><Code>RequestLimitExceeded</Code><Message>throttled</Message></Error></Errors><RequestID>test</RequestID></Response>`)
						return
					}
					// EC2 may have accepted this exact body. Drop the connection
					// before its successful response reaches the client.
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "text/xml")
				_, _ = io.WriteString(w, fleetWireSuccess("spot"))
			}))
			defer server.Close()
			out, err := fleetWireClient(server, 2).CreateFleet(context.Background(), input)
			if err != nil || out == nil || aws.ToString(out.FleetId) != fleetWireID {
				t.Fatalf("SDK retry failed: out=%+v err=%v", out, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("got %d dispatches; want the initial request and one bounded retry", len(bodies))
			}
			if bodies[0] != bodies[1] {
				t.Fatal("SDK retry changed the serialized request body")
			}
			for i, body := range bodies {
				params, err := url.ParseQuery(body)
				if err != nil || params.Get("ClientToken") != attempt.ClientToken || params.Get("Action") != "CreateFleet" {
					t.Fatalf("dispatch %d did not preserve the attempt token: %v", i+1, err)
				}
			}
		})
	}
}

// An explicitly empty result differs from an absent result: recovery must never
// infer proven zero capacity from a response whose fulfillment set was omitted.
func TestFleetSDKPreservesMissingVersusEmptyResponseCollections(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("collections-present-%t", present), func(t *testing.T) {
			plan, attempt := fleetWireFixture(t, false)
			input, err := BuildFleetInput(plan, attempt)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/xml")
				collections := ""
				if present {
					collections = "<fleetInstanceSet/><errorSet/>"
				}
				_, _ = fmt.Fprintf(w, `<CreateFleetResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>test-request</requestId><fleetId>%s</fleetId>%s</CreateFleetResponse>`, fleetWireID, collections)
			}))
			defer server.Close()
			out, err := fleetWireClient(server, 1).CreateFleet(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if (out.Instances != nil) != present || (out.Errors != nil) != present || len(out.Instances) != 0 || len(out.Errors) != 0 {
				t.Fatalf("SDK lost collection presence: %+v", out)
			}
		})
	}
}
