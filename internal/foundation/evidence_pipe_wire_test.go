package foundation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pipes"
)

type pipeHTTPFunc func(*http.Request) (*http.Response, error)

func (f pipeHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// The real DescribePipe response observed during #48 includes
// EnrichmentParameters:{} without an Enrichment ARN. Exercise the pinned SDK's
// HTTP decoder: plain encoding/json or a hand-built output misses its allocation
// of a nonnil parameter struct for that empty object. No AWS calls are made.
func TestEvidenceRoutePipeSDKEnrichmentWireShapes(t *testing.T) {
	for _, tc := range []struct {
		name, parameters, enrichment string
		valid                        bool
	}{
		{"omitted", "", "", true},
		{"live empty object", `{}`, "", true},
		{"removed input template", `{"InputTemplate":""}`, "", true},
		{"configured input template", `{"InputTemplate":"{}"}`, "", false},
		{"empty HTTP configuration", `{"HttpParameters":{}}`, "", false},
		{"HTTP headers", `{"HttpParameters":{"HeaderParameters":{"x-test":"value"}}}`, "", false},
		{"HTTP query", `{"HttpParameters":{"QueryStringParameters":{"test":"value"}}}`, "", false},
		{"HTTP path", `{"HttpParameters":{"PathParameterValues":["value"]}}`, "", false},
		{"enrichment ARN", "", "arn:aws:lambda:us-east-2:123456789012:function:other", false},
		{"enrichment ARN with empty parameters", `{}`, "arn:aws:lambda:us-east-2:123456789012:function:other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, _, e, clients := evidenceFixture(t)
			var response map[string]json.RawMessage
			if err := json.Unmarshal([]byte(f.responses["DescribePipe"]), &response); err != nil {
				t.Fatal(err)
			}
			if tc.parameters != "" {
				response["EnrichmentParameters"] = json.RawMessage(tc.parameters)
			}
			if tc.enrichment != "" {
				response["Enrichment"], _ = json.Marshal(tc.enrichment)
			}
			wire, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			api := pipes.NewFromConfig(aws.Config{
				Region: m.Region, Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: pipeHTTPFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != http.MethodGet || r.URL.Path != "/v1/pipes/"+e.Pipe.Name {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(wire)))}, nil
				}),
			})
			decoded, err := api.DescribePipe(context.Background(), &pipes.DescribePipeInput{Name: aws.String(e.Pipe.Name)})
			if err != nil {
				t.Fatal(err)
			}
			if (decoded.EnrichmentParameters != nil) != (tc.parameters != "") {
				t.Fatal("SDK did not preserve parameter object presence")
			}
			if tc.parameters == `{}` && (decoded.EnrichmentParameters.HttpParameters != nil || decoded.EnrichmentParameters.InputTemplate != nil) {
				t.Fatal("empty wire object unexpectedly configured enrichment")
			}
			clients.Pipe = api
			err = checkEvidenceRoute(context.Background(), clients, e)
			if (err == nil) != tc.valid {
				t.Fatalf("route valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
			if calls != 2 {
				t.Fatalf("unexpected Pipe request count: %d", calls)
			}
			_, queueChecked := f.inputs["GetQueueAttributes"]
			_, streamChecked := f.inputs["DescribeLogStreams"]
			if queueChecked != tc.valid || streamChecked != tc.valid {
				t.Fatal("route did not reject enrichment before, or validate queue/stream after, the Pipe check")
			}
		})
	}
}
