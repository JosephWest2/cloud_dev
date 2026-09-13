package execprotocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/sso/types"
	ssooidctypes "github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type storeAPI struct {
	getErr        error
	get           func(int) (*s3.GetObjectOutput, error)
	getCalls      int
	getKeys       []string
	getOwners     []string
	listed        *s3.ListObjectsV2Output
	listErr       error
	listCalls     int
	prefix, owner string
}

func (a *storeAPI) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	a.getCalls++
	a.getKeys = append(a.getKeys, aws.ToString(in.Key))
	a.getOwners = append(a.getOwners, aws.ToString(in.ExpectedBucketOwner))
	if a.get != nil {
		return a.get(a.getCalls)
	}
	return nil, a.getErr
}
func (a *storeAPI) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, errors.New("not implemented")
}
func (a *storeAPI) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	a.listCalls++
	a.prefix = aws.ToString(in.Prefix)
	a.owner = aws.ToString(in.ExpectedBucketOwner)
	if aws.ToInt32(in.MaxKeys) != 1 {
		panic("listing not bounded")
	}
	return a.listed, a.listErr
}
func TestDeniedReadRequiresAuthoritativeAbsenceEvidence(t *testing.T) {
	scope := sampleRecord("request").Scope
	key := ObjectKey(scope, sampleRecord("request").CommandID, "result.json")
	for _, tc := range []struct {
		name     string
		contents []types.Object
		listErr  error
		want     error
	}{
		{"missing", nil, nil, ErrNotFound}, {"present", []types.Object{{Key: aws.String(key)}}, nil, ErrDenied}, {"denied", nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "SECRET"}, ErrDenied},
		{"list unavailable", nil, errors.New("SECRET"), ErrUnavailable},
		{"list credentials expired", nil, &smithy.GenericAPIError{Code: "ExpiredToken", Message: "SECRET"}, ErrCredentialsExpired},
		{"list canceled", nil, fmt.Errorf("SECRET: %w", context.Canceled), context.Canceled},
		{"list deadline", nil, fmt.Errorf("SECRET: %w", context.DeadlineExceeded), context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &storeAPI{getErr: &smithy.GenericAPIError{Code: "AccessDenied"}, listed: &s3.ListObjectsV2Output{Name: aws.String("test-bucket"), Prefix: aws.String(key), Contents: tc.contents}, listErr: tc.listErr}
			s := S3Store{api, "test-bucket", scope.Account, BasePrefix(scope)}
			_, err := s.Get(context.Background(), key)
			if !errors.Is(err, tc.want) || api.listCalls != 1 || api.prefix != key || api.owner != scope.Account {
				t.Fatalf("absence evidence %v %+v", err, api)
			}
			wantGets := 1
			if tc.name == "present" {
				wantGets = 2
			}
			if api.getCalls != wantGets || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unbounded reads or leaked diagnostic: calls=%d err=%v", api.getCalls, err)
			}
		})
	}
}

func TestDeniedReadListedPublicationRequiresOneExactReread(t *testing.T) {
	r := sampleRecord("result")
	key := ObjectKey(r.Scope, r.CommandID, "result.json")
	data, _ := EncodeRecord(r)
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"published between get and list", nil, nil},
		{"still denied", &smithy.GenericAPIError{Code: "AccessDenied", Message: "SECRET"}, ErrDenied},
		{"deleted between list and get", &smithy.GenericAPIError{Code: "NoSuchKey", Message: "SECRET"}, ErrNotFound},
		{"reread unavailable", errors.New("SECRET"), ErrUnavailable},
		{"reread canceled", fmt.Errorf("SECRET: %w", context.Canceled), context.Canceled},
		{"reread invalid credentials", &smithy.GenericAPIError{Code: "InvalidToken", Message: "SECRET"}, ErrCredentialsInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &storeAPI{listed: &s3.ListObjectsV2Output{Name: aws.String("test-bucket"), Prefix: aws.String(key), Contents: []types.Object{{Key: aws.String(key)}}}}
			api.get = func(n int) (*s3.GetObjectOutput, error) {
				if n == 1 {
					return nil, &smithy.GenericAPIError{Code: "AccessDenied"}
				}
				if tc.err != nil {
					return nil, tc.err
				}
				return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data)), ContentLength: aws.Int64(int64(len(data))), LastModified: aws.Time(time.Now())}, nil
			}
			s := S3Store{api, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
			got, _, err := ReadRecord(context.Background(), s, key)
			if !errors.Is(err, tc.want) || api.getCalls != 2 || api.listCalls != 1 {
				t.Fatalf("reread: err=%v gets=%d lists=%d", err, api.getCalls, api.listCalls)
			}
			if err == nil && got.CommandID != r.CommandID {
				t.Fatal("successful listing bypassed actual record retrieval")
			}
			for i := range api.getKeys {
				if api.getKeys[i] != key || api.getOwners[i] != r.Scope.Account {
					t.Fatal("reread changed exact key or expected owner")
				}
			}
		})
	}
}

func TestDeniedReadRejectsUntrustworthyListing(t *testing.T) {
	r := sampleRecord("result")
	key := ObjectKey(r.Scope, r.CommandID, "result.json")
	for _, tc := range []struct {
		name   string
		change func(*s3.ListObjectsV2Output)
	}{
		{"wrong bucket", func(o *s3.ListObjectsV2Output) { o.Name = aws.String("other") }},
		{"wrong prefix", func(o *s3.ListObjectsV2Output) { o.Prefix = aws.String(key + "/") }},
		{"unrelated key", func(o *s3.ListObjectsV2Output) { o.Contents = []types.Object{{Key: aws.String("other")}} }},
		{"too many keys", func(o *s3.ListObjectsV2Output) {
			o.Contents = []types.Object{{Key: aws.String(key)}, {Key: aws.String(key)}}
		}},
		{"empty truncated list", func(o *s3.ListObjectsV2Output) { o.IsTruncated = aws.Bool(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed := &s3.ListObjectsV2Output{Name: aws.String("test-bucket"), Prefix: aws.String(key)}
			tc.change(listed)
			api := &storeAPI{getErr: &smithy.GenericAPIError{Code: "AccessDenied"}, listed: listed}
			s := S3Store{api, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
			if _, err := s.Get(context.Background(), key); err != ErrDenied || api.getCalls != 1 || api.listCalls != 1 {
				t.Fatalf("untrustworthy listing accepted: err=%v gets=%d lists=%d", err, api.getCalls, api.listCalls)
			}
		})
	}
}
func TestSDKConditionalClaimNeverRetriesLostResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "PUT" || r.Header.Get("If-None-Match") != "*" || r.Header.Get("X-Amz-Expected-Bucket-Owner") != "123456789012" || r.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" || r.Header.Get("X-Amz-Checksum-Sha256") == "" {
			t.Errorf("unsafe conditional PUT headers: %v", r.Header)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), Retryer: func() aws.Retryer { return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 5 }) }}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	r := sampleRecord("started")
	s := S3Store{client, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
	b, _ := EncodeRecord(r)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.Put(ctx, ObjectKey(r.Scope, r.CommandID, "started.json"), bytes.NewReader(b), int64(len(b)), Digest(b))
	if err == nil || calls.Load() != 1 {
		t.Fatalf("uncertain claim retried or succeeded: calls=%d err=%v", calls.Load(), err)
	}
}

type reconcileStore struct {
	data               []byte
	putErr             error
	getErr             error
	size               int64
	putCalls, getCalls int
}

func (s *reconcileStore) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	s.putCalls++
	return s.putErr
}
func (s *reconcileStore) Get(context.Context, string) (Object, error) {
	s.getCalls++
	return Object{io.NopCloser(bytes.NewReader(s.data)), s.size, time.Now()}, s.getErr
}
func TestImmutablePublicationReconciliationVerifiesFullBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   string
		size   int64
		denied bool
		ok     bool
	}{
		{"exact", "body", 4, false, true}, {"short", "bod", 4, false, false}, {"extra", "body!", 4, false, false}, {"hash", "bad!", 4, false, false}, {"size", "body", 3, false, false}, {"denied", "body", 4, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &reconcileStore{data: []byte(tc.data), size: tc.size, putErr: ErrUnavailable}
			if tc.denied {
				s.getErr = ErrDenied
			}
			err := PutImmutable(context.Background(), s, "key", strings.NewReader("body"), 4, Digest([]byte("body")))
			if (err == nil) != tc.ok || s.putCalls != 1 || s.getCalls != 1 {
				t.Fatalf("reconciliation: %v %+v", err, s)
			}
		})
	}
}

func TestSDKCredentialProviderFailuresAreSanitizedAndClassified(t *testing.T) {
	r := sampleRecord("request")
	key := ObjectKey(r.Scope, r.CommandID, "request.json")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"local helper", errors.New("SECRET provider stderr"), ErrCredentialsUnavailable},
		{"expired", &smithy.GenericAPIError{Code: "ExpiredToken", Message: "SECRET"}, ErrCredentialsExpired},
		{"invalid", &smithy.GenericAPIError{Code: "InvalidClientTokenId", Message: "SECRET"}, ErrCredentialsInvalid},
		{"denied role", &smithy.GenericAPIError{Code: "AccessDenied", Message: "SECRET"}, ErrCredentialsUnavailable},
		{"SSO access token", fmt.Errorf("SECRET cached credentials: %w", &ssotypes.UnauthorizedException{Message: aws.String("SECRET access token")}), ErrCredentialsUnavailable},
		{"SSO refresh grant", fmt.Errorf("SECRET bearer refresh: %w", &ssooidctypes.InvalidGrantException{Message: aws.String("SECRET refresh token")}), ErrCredentialsUnavailable},
		{"SSO client", &ssooidctypes.InvalidClientException{Message: aws.String("SECRET client")}, ErrCredentialsUnavailable},
		{"SSO throttle", &ssotypes.TooManyRequestsException{Message: aws.String("SECRET")}, ErrUnavailable},
		{"unknown source client failure", &smithy.GenericAPIError{Code: "NewClientFailure", Fault: smithy.FaultClient, Message: "SECRET"}, ErrCredentialsUnavailable},
		{"unknown source server failure", &smithy.GenericAPIError{Code: "NewServerFailure", Fault: smithy.FaultServer, Message: "SECRET"}, ErrUnavailable},
		{"source not found", &smithy.GenericAPIError{Code: "NotFound", Message: "SECRET"}, ErrCredentialsUnavailable},
		{"service unavailable", &smithy.GenericAPIError{Code: "ServiceUnavailable", Message: "SECRET"}, ErrUnavailable},
		{"refresh connection lost", &smithyhttp.RequestSendError{Err: errors.New("SECRET")}, ErrUnavailable},
		{"refresh network", &url.Error{Op: "POST", URL: "https://SECRET.invalid", Err: errors.New("SECRET")}, ErrUnavailable},
		{"refresh interrupted response", fmt.Errorf("SECRET: %w", io.ErrUnexpectedEOF), ErrUnavailable},
		{"canceled", fmt.Errorf("SECRET: %w", context.Canceled), context.Canceled},
		{"deadline", fmt.Errorf("SECRET: %w", context.DeadlineExceeded), context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wireCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wireCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{}, tc.err
			})
			client := s3.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: provider, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
			s := S3Store{client, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
			_, getErr := s.Get(context.Background(), key)
			putErr := s.Put(context.Background(), key, strings.NewReader("body"), 4, Digest([]byte("body")))
			for _, err := range []error{getErr, putErr} {
				if err != tc.want || strings.Contains(err.Error(), "SECRET") {
					t.Fatalf("credential classification: got=%v want=%v", err, tc.want)
				}
			}
			if wireCalls.Load() != 0 {
				t.Fatalf("credential failure reached resource endpoint: %d", wireCalls.Load())
			}
		})
	}
}

func TestSDKStorageCredentialsPreserveSharedCache(t *testing.T) {
	r := sampleRecord("request")
	key := ObjectKey(r.Scope, r.CommandID, "request.json")
	var retrievals, gets atomic.Int32
	provider := aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		retrievals.Add(1)
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprintf(w, "<ListBucketResult><Name>test-bucket</Name><Prefix>%s</Prefix><IsTruncated>false</IsTruncated></ListBucketResult>", key)
			return
		}
		if r.Method == "GET" {
			gets.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>SECRET</Message></Error>")
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: provider, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	s := S3Store{client, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
	for range 2 {
		if _, err := s.Get(context.Background(), key); err != ErrNotFound {
			t.Fatalf("missing GET/LIST classification: %v", err)
		}
	}
	if err := s.Put(context.Background(), key, strings.NewReader("body"), 4, Digest([]byte("body"))); err != nil {
		t.Fatal(err)
	}
	if retrievals.Load() != 1 || gets.Load() != 2 {
		t.Fatalf("provider cache replaced: retrieves=%d gets=%d", retrievals.Load(), gets.Load())
	}
	if SanitizedCredentials(nil) != nil {
		t.Fatal("nil credential provider changed")
	}
}

func TestSDKStorageAPIErrorsDoNotLeakOrLookMissing(t *testing.T) {
	r := sampleRecord("request")
	key := ObjectKey(r.Scope, r.CommandID, "request.json")
	for _, tc := range []struct {
		code   string
		status int
		want   error
	}{
		{"ExpiredToken", http.StatusForbidden, ErrCredentialsExpired},
		{"RequestExpired", http.StatusBadRequest, ErrCredentialsExpired},
		{"InvalidAccessKeyId", http.StatusForbidden, ErrCredentialsInvalid},
		{"SignatureDoesNotMatch", http.StatusForbidden, ErrCredentialsInvalid},
		{"RequestTimeTooSkewed", http.StatusForbidden, ErrCredentialsInvalid},
		{"SlowDown", http.StatusServiceUnavailable, ErrUnavailable},
		{"InternalError", http.StatusInternalServerError, ErrUnavailable},
		{"SECRET unknown code", http.StatusBadRequest, ErrUnavailable},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>SECRET</Message><RequestId>SECRET</RequestId></Error>", tc.code)
			}))
			defer server.Close()
			client := s3.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), Retryer: func() aws.Retryer { return aws.NopRetryer{} }}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
			s := S3Store{client, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
			if _, err := s.Get(context.Background(), key); err != tc.want || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("API classification: got=%v want=%v", err, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("authentication/transport failure triggered absence lookup: %d calls", calls.Load())
			}
		})
	}
}

type interruptedRecordBody struct {
	io.Reader
	err    error
	closed bool
}

func (b *interruptedRecordBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF && b.err != nil {
		return n, b.err
	}
	return n, err
}

func (b *interruptedRecordBody) Close() error { b.closed = true; return nil }

type recordBodyStore struct{ object Object }

func (s recordBodyStore) Get(context.Context, string) (Object, error) { return s.object, nil }
func (s recordBodyStore) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	return ErrUnavailable
}

func TestReadRecordDistinguishesInterruptedTransportFromCorruption(t *testing.T) {
	r := sampleRecord("result")
	data, _ := EncodeRecord(r)
	key := ObjectKey(r.Scope, r.CommandID, "result.json")
	for _, tc := range []struct {
		name string
		data []byte
		size int64
		err  error
		want error
	}{
		{"complete", data, int64(len(data)), nil, nil},
		{"network interrupted", data[:40], int64(len(data)), errors.New("SECRET socket failure"), ErrUnavailable},
		{"unexpected EOF", data[:40], int64(len(data)), io.ErrUnexpectedEOF, ErrUnavailable},
		{"full bytes then transport error", data, int64(len(data)), errors.New("SECRET"), ErrUnavailable},
		{"body canceled", data[:40], int64(len(data)), fmt.Errorf("SECRET: %w", context.Canceled), context.Canceled},
		{"body deadline", data[:40], int64(len(data)), fmt.Errorf("SECRET: %w", context.DeadlineExceeded), context.DeadlineExceeded},
		{"graceful short body", data[:40], int64(len(data)), nil, ErrCorrupt},
		{"extra bytes", append(append([]byte(nil), data...), ' '), int64(len(data)), nil, ErrCorrupt},
		{"malformed success", []byte("SECRET"), 6, nil, ErrCorrupt},
		{"oversized advertised", nil, MaxRecordBytes + 1, nil, ErrCorrupt},
		{"oversized actual", bytes.Repeat([]byte(" "), MaxRecordBytes+1), MaxRecordBytes, nil, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &interruptedRecordBody{Reader: bytes.NewReader(tc.data), err: tc.err}
			s := recordBodyStore{Object{body, tc.size, time.Now()}}
			got, _, err := ReadRecord(context.Background(), s, key)
			if !errors.Is(err, tc.want) || !body.closed {
				t.Fatalf("read classification: got=%v want=%v closed=%v", err, tc.want, body.closed)
			}
			if err != nil && (strings.Contains(err.Error(), "SECRET") || got.CommandID != "") {
				t.Fatal("failed body read exposed raw diagnostics or partial metadata")
			}
		})
	}
}

func TestSDKResponseBodyDisconnectRemainsRetryable(t *testing.T) {
	r := sampleRecord("result")
	data, _ := EncodeRecord(r)
	key := ObjectKey(r.Scope, r.CommandID, "result.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		_, _ = w.Write(data[:40])
	}))
	defer server.Close()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), Retryer: func() aws.Retryer { return aws.NopRetryer{} }}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	s := S3Store{client, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
	if _, _, err := ReadRecord(context.Background(), s, key); err != ErrUnavailable {
		t.Fatalf("HTTP body disconnect classified as corruption: %v", err)
	}
}

func TestBodyReadCancellationPreservesContextWithoutLeaking(t *testing.T) {
	r := sampleRecord("request")
	key := ObjectKey(r.Scope, r.CommandID, "request.json")
	for _, operation := range []string{"read", "reconcile"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			body := &interruptedRecordBody{Reader: strings.NewReader("body"), err: errors.New("SECRET")}
			s := recordBodyStore{Object{body, 4, time.Now()}}
			var err error
			if operation == "read" {
				_, _, err = ReadRecord(ctx, s, key)
			} else {
				err = PutImmutable(ctx, s, key, strings.NewReader("body"), 4, Digest([]byte("body")))
			}
			if err != context.Canceled || !body.closed {
				t.Fatalf("body cancellation: err=%v closed=%v", err, body.closed)
			}
		})
	}
}

func TestCredentialDenialNeverTriggersMissingKeyFallback(t *testing.T) {
	r := sampleRecord("request")
	key := ObjectKey(r.Scope, r.CommandID, "request.json")
	var retrievals, wireCalls atomic.Int32
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		if retrievals.Add(1) == 1 {
			return aws.Credentials{}, &smithy.GenericAPIError{Code: "AccessDenied", Message: "SECRET role denial"}
		}
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		wireCalls.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, "<ListBucketResult><Name>test-bucket</Name><Prefix>%s</Prefix><IsTruncated>false</IsTruncated></ListBucketResult>", key)
	}))
	defer server.Close()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: provider, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	s := S3Store{client, "test-bucket", r.Scope.Account, BasePrefix(r.Scope)}
	_, err := s.Get(context.Background(), key)
	if err != ErrCredentialsUnavailable || retrievals.Load() != 1 || wireCalls.Load() != 0 {
		t.Fatalf("provider error entered S3 absence fallback: retrieves=%d calls=%d err=%v", retrievals.Load(), wireCalls.Load(), err)
	}
}
