package execprotocol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type storeAPI struct {
	getErr        error
	listed        *s3.ListObjectsV2Output
	listErr       error
	listCalls     int
	prefix, owner string
}

func (a *storeAPI) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
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
		{"missing", nil, nil, ErrNotFound}, {"present", []types.Object{{Key: aws.String(key)}}, nil, ErrDenied}, {"denied", nil, errors.New("denied"), ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &storeAPI{getErr: &smithy.GenericAPIError{Code: "AccessDenied"}, listed: &s3.ListObjectsV2Output{Name: aws.String("test-bucket"), Prefix: aws.String(key), Contents: tc.contents}, listErr: tc.listErr}
			s := S3Store{api, "test-bucket", scope.Account, BasePrefix(scope)}
			_, err := s.Get(context.Background(), key)
			if !errors.Is(err, tc.want) || api.listCalls != 1 || api.prefix != key || api.owner != scope.Account {
				t.Fatalf("absence evidence %v %+v", err, api)
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
