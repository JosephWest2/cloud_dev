package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type launchWireRequest struct {
	method, key string
	header      http.Header
	body        []byte
}

// The server commits conditional writes before simulating a lost acknowledgment.
// Its request log therefore distinguishes an immutable object from permission
// that this particular client positively received.
type launchWireStore struct {
	mu       sync.Mutex
	bucket   string
	objects  map[string][]byte
	requests []launchWireRequest
	failKey  string
	failMode string
}

func (s *launchWireStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/"+s.bucket+"/")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, launchWireRequest{method: r.Method, key: key, header: r.Header.Clone(), body: body})
	if !strings.HasPrefix(r.URL.Path, "/"+s.bucket+"/") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	switch r.Method {
	case http.MethodGet:
		stored, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(stored)))
		_, _ = w.Write(stored)
	case http.MethodPut:
		if r.Header.Get("If-None-Match") != "*" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, exists := s.objects[key]; exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code><Message>immutable</Message></Error>`)
			return
		}
		s.objects[key] = append([]byte(nil), body...)
		if key == s.failKey {
			switch s.failMode {
			case "409", "412":
				status, code := http.StatusConflict, "ConditionalRequestConflict"
				if s.failMode == "412" {
					status, code = http.StatusPreconditionFailed, "PreconditionFailed"
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `<Error><Code>`+code+`</Code><Message>concurrent claim</Message></Error>`)
				return
			case "503":
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `<Error><Code>SlowDown</Code><Message>acknowledgment lost</Message></Error>`)
				return
			case "disconnect":
				conn, _, hijackErr := w.(http.Hijacker).Hijack()
				if hijackErr == nil {
					_ = conn.Close()
				}
				return
			}
		}
		w.Header().Set("ETag", `"immutable-record"`)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *launchWireStore) log() []launchWireRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]launchWireRequest(nil), s.requests...)
}

func launchWireClient(server *httptest.Server, credentials aws.CredentialsProvider) *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region: "us-east-2", Credentials: credentials, BaseEndpoint: aws.String(server.URL), HTTPClient: server.Client(),
		Retryer: func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = 5
				o.MaxBackoff = time.Nanosecond
			})
		},
	}, func(o *s3.Options) { o.UsePathStyle = true })
}

func launchWireFixture(t *testing.T) (config.Config, BatchReceipt, PreparedAttempt, DispatchClaim, AttemptResponse) {
	t.Helper()
	c, manifest, profile, selection := batchFixture(t)
	receipt, err := NewBatchReceipt(c, manifest, profile, selection)
	if err != nil {
		t.Fatal(err)
	}
	attempt := receipt.Attempts[0]
	input, err := BuildFleetInput(receipt.Plan, attempt)
	if err != nil {
		t.Fatal(err)
	}
	prepared := PreparedAttempt{SchemaVersion: 1, RequestID: receipt.RequestID, PlanSHA256: receipt.PlanSHA256, InputSHA256: fleetInputDigest(input), Attempt: attempt}
	claim := DispatchClaim{SchemaVersion: 1, RequestID: receipt.RequestID, AttemptID: attempt.AttemptID, PlanSHA256: receipt.PlanSHA256, InputSHA256: prepared.InputSHA256, ClientToken: attempt.ClientToken}
	attempt.State = "complete"
	attempt.FleetID = fleetWireID
	attempt.InstanceIDs = []string{}
	diagnostic, _ := fleetDiagnostic("InsufficientInstanceCapacity")
	attempt.Errors = []ResourceError{diagnostic}
	response := AttemptResponse{SchemaVersion: 1, RequestID: receipt.RequestID, PlanSHA256: receipt.PlanSHA256, InputSHA256: prepared.InputSHA256, Attempt: attempt, Workers: []WorkerOutcome{}}
	return c, receipt, prepared, claim, response
}

func newLaunchWireLedger(t *testing.T, c config.Config, descriptor config.LaunchLedger, client *s3.Client) *S3LaunchLedger {
	t.Helper()
	ledger, err := NewS3LaunchLedger(client, c, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestLaunchLedgerSDKImmutableHeadersAndRecords(t *testing.T) {
	c, receipt, prepared, claim, response := launchWireFixture(t)
	descriptor := receipt.Plan.LaunchLedger
	store := &launchWireStore{bucket: descriptor.Bucket, objects: map[string][]byte{}}
	server := httptest.NewServer(store)
	defer server.Close()
	ledger := newLaunchWireLedger(t, c, descriptor, launchWireClient(server, aws.AnonymousCredentials{}))
	ctx := context.Background()
	if err := ledger.Prepare(ctx, receipt, prepared); err != nil {
		t.Fatal(err)
	}
	if won, err := ledger.Claim(ctx, descriptor, claim); err != nil || !won {
		t.Fatalf("new claim was not acknowledged: won=%v err=%v", won, err)
	}
	if err := ledger.RecordResponse(ctx, descriptor, response); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Load(ctx, receipt.RequestID); err != nil {
		t.Fatal(err)
	}
	base := descriptor.Prefix + receipt.RequestID + "/"
	attemptBase := base + "attempts/" + claim.AttemptID + "/"
	wantRecords := map[string]any{
		base + "plan.json":            receipt.Plan,
		attemptBase + "prepared.json": prepared,
		attemptBase + "dispatch.json": claim,
		attemptBase + "response.json": response,
	}
	puts := map[string]int{}
	for _, request := range store.log() {
		if request.header.Get("X-Amz-Expected-Bucket-Owner") != descriptor.ExpectedBucketOwner {
			t.Errorf("%s %s lacks pinned bucket owner", request.method, request.key)
		}
		if request.method != http.MethodPut {
			if request.method != http.MethodGet {
				t.Errorf("unexpected ledger method %s", request.method)
			}
			continue
		}
		puts[request.key]++
		want, exists := wantRecords[request.key]
		if !exists {
			t.Errorf("unexpected record key %q", request.key)
			continue
		}
		if request.header.Get("If-None-Match") != "*" || request.header.Get("X-Amz-Server-Side-Encryption") != "AES256" {
			t.Errorf("record %s lacks immutable encrypted write headers", request.key)
		}
		sum := sha256.Sum256(request.body)
		if request.header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Errorf("record %s lacks exact body SHA256 checksum", request.key)
		}
		wantBody, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if string(request.body) != string(wantBody) {
			t.Errorf("record %s differs from its canonical JSON", request.key)
		}
	}
	for key := range wantRecords {
		if puts[key] != 1 {
			t.Errorf("record %s has %d wire PUTs, want one", key, puts[key])
		}
	}
	// An independent client can read the winner's record but cannot acquire it.
	other := newLaunchWireLedger(t, c, descriptor, launchWireClient(server, aws.AnonymousCredentials{}))
	if won, _ := other.Claim(ctx, descriptor, claim); won {
		t.Fatal("recovered existing claim authorized another dispatch")
	}
}

func TestLaunchLedgerSDKUnsuccessfulClaimNeverAuthorizes(t *testing.T) {
	for _, mode := range []string{"503", "disconnect", "409", "412"} {
		t.Run(mode, func(t *testing.T) {
			c, receipt, prepared, claim, _ := launchWireFixture(t)
			descriptor := receipt.Plan.LaunchLedger
			key := descriptor.Prefix + receipt.RequestID + "/attempts/" + claim.AttemptID + "/dispatch.json"
			store := &launchWireStore{bucket: descriptor.Bucket, objects: map[string][]byte{}, failKey: key, failMode: mode}
			server := httptest.NewServer(store)
			defer server.Close()
			ledger := newLaunchWireLedger(t, c, descriptor, launchWireClient(server, aws.AnonymousCredentials{}))
			ctx := context.Background()
			if err := ledger.Prepare(ctx, receipt, prepared); err != nil {
				t.Fatal(err)
			}
			start := len(store.log())
			if won, _ := ledger.Claim(ctx, descriptor, claim); won {
				t.Fatal("unacknowledged claim authorized dispatch")
			}
			requests := store.log()[start:]
			putCount, wrote := 0, false
			for _, request := range requests {
				if request.method == http.MethodPut && request.key == key {
					putCount++
					wrote = true
					continue
				}
				if wrote {
					t.Errorf("request %s %s followed the failed claim write", request.method, request.key)
				}
			}
			if putCount != 1 {
				t.Fatalf("claim made %d wire PUTs with a five-attempt client", putCount)
			}
			store.mu.Lock()
			committed := append([]byte(nil), store.objects[key]...)
			store.mu.Unlock()
			want, _ := json.Marshal(claim)
			if !reflect.DeepEqual(committed, want) {
				t.Fatal("lost acknowledgment fixture did not durably commit the claim")
			}
			other := newLaunchWireLedger(t, c, descriptor, launchWireClient(server, aws.AnonymousCredentials{}))
			if won, _ := other.Claim(ctx, descriptor, claim); won {
				t.Fatal("another computer authorized dispatch from an unacknowledged existing claim")
			}
		})
	}
}

func TestLaunchLedgerSDKOtherImmutableWritesHaveOneAttempt(t *testing.T) {
	for _, record := range []string{"plan", "prepared", "response"} {
		t.Run(record, func(t *testing.T) {
			c, receipt, prepared, claim, response := launchWireFixture(t)
			descriptor := receipt.Plan.LaunchLedger
			key := descriptor.Prefix + receipt.RequestID + "/plan.json"
			if record != "plan" {
				key = descriptor.Prefix + receipt.RequestID + "/attempts/" + claim.AttemptID + "/" + record + ".json"
			}
			store := &launchWireStore{bucket: descriptor.Bucket, objects: map[string][]byte{}, failKey: key, failMode: "503"}
			server := httptest.NewServer(store)
			defer server.Close()
			ledger := newLaunchWireLedger(t, c, descriptor, launchWireClient(server, aws.AnonymousCredentials{}))
			ctx := context.Background()
			err := ledger.Prepare(ctx, receipt, prepared)
			if record == "response" {
				if err != nil {
					t.Fatal(err)
				}
				if won, err := ledger.Claim(ctx, descriptor, claim); err != nil || !won {
					t.Fatalf("could not prepare response fixture: won=%v err=%v", won, err)
				}
				_ = ledger.RecordResponse(ctx, descriptor, response)
			}
			puts := 0
			for _, request := range store.log() {
				if request.method == http.MethodPut && request.key == key {
					puts++
				}
			}
			if puts != 1 {
				t.Fatalf("%s made %d wire PUTs with a five-attempt client", record, puts)
			}
		})
	}
}

func TestLaunchLedgerSDKCredentialErrorsArePrivate(t *testing.T) {
	c, receipt, prepared, claim, response := launchWireFixture(t)
	descriptor := receipt.Plan.LaunchLedger
	store := &launchWireStore{bucket: descriptor.Bucket, objects: map[string][]byte{}}
	server := httptest.NewServer(store)
	defer server.Close()
	const secret = "credential-provider-secret-do-not-print"
	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New(secret)
	})
	ledger := newLaunchWireLedger(t, c, descriptor, launchWireClient(server, credentials))
	ctx := context.Background()
	cases := map[string]func() error{
		"prepare": func() error { return ledger.Prepare(ctx, receipt, prepared) },
		"claim": func() error {
			won, err := ledger.Claim(ctx, descriptor, claim)
			if won {
				t.Error("failed credentials authorized a claim")
			}
			return err
		},
		"response": func() error { return ledger.RecordResponse(ctx, descriptor, response) },
		"load":     func() error { _, err := ledger.Load(ctx, receipt.RequestID); return err },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("credential failure missing or not sanitized: %v", err)
			}
		})
	}
	if requests := store.log(); len(requests) != 0 {
		t.Fatalf("failed credentials made %d wire requests", len(requests))
	}
}
