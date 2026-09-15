package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// Historical serializer regression only: direct SDK retries retain the original
// wire input. The lifecycle allocator no longer sends RunInstances.
func TestLegacyRunInstancesSerializerRetainsTokenAndParameters(t *testing.T) {
	s, m, p, _, _ := setupUp(t)
	receipt, err := newReceipt(parameters(s.Scope, m, p, "smoke"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var first url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if calls == 1 {
			first = r.PostForm
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`<Response><Errors><Error><Code>ServiceUnavailable</Code><Message>SECRET service unavailable</Message></Error></Errors><RequestID>test</RequestID></Response>`))
			return
		}
		if !reflect.DeepEqual(first, r.PostForm) {
			t.Error("SDK retry changed parameters")
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<RunInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>test</requestId><reservationId>r-12345678</reservationId><ownerId>123456789012</ownerId><instancesSet><item><instanceId>i-12345678</instanceId></item></instancesSet></RunInstancesResponse>`))
	}))
	defer server.Close()
	client := ec2.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: aws.AnonymousCredentials{}, BaseEndpoint: aws.String(server.URL), Retryer: func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 2; o.MaxBackoff = time.Nanosecond })
	}})
	out, err := client.RunInstances(context.Background(), launchInput(receipt))
	if err != nil || out == nil || len(out.Instances) != 1 || calls != 2 {
		t.Fatalf("SDK retry: calls=%d out=%+v err=%v", calls, out, err)
	}
	for k, v := range map[string]string{"Action": "RunInstances", "ClientToken": receipt.ClientToken, "MinCount": "1", "MaxCount": "1", "ImageId": receipt.Parameters.Image.AMIID, "LaunchTemplate.LaunchTemplateId": receipt.Parameters.Image.LaunchTemplateID, "LaunchTemplate.Version": receipt.Parameters.Image.LaunchTemplateVersion, "BlockDeviceMapping.1.Ebs.Encrypted": "true", "BlockDeviceMapping.1.Ebs.DeleteOnTermination": "true", "MetadataOptions.HttpTokens": "required", "TagSpecification.1.ResourceType": "instance", "TagSpecification.2.ResourceType": "volume"} {
		if first.Get(k) != v {
			t.Errorf("serialized %s=%q, want %q", k, first.Get(k), v)
		}
	}
	if first.Has("InstanceMarketOptions.MarketType") {
		t.Fatal("unexpected market override")
	}
}
