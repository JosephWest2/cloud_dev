package execprotocol

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var (
	ErrNotFound    = errors.New("result object not found")
	ErrDenied      = errors.New("result storage access denied")
	ErrConflict    = errors.New("immutable result object conflicts")
	ErrUnavailable = errors.New("result storage unavailable")
	ErrCorrupt     = errors.New("result object identity or content differs")
	// Credential failures are terminal for the current observation. Callers can
	// refresh the selected profile and recover by ID without dispatching again.
	ErrCredentialsExpired     = errors.New("result storage credentials expired")
	ErrCredentialsInvalid     = errors.New("result storage credentials invalid")
	ErrCredentialsUnavailable = errors.New("result storage credentials unavailable")
)

type Object struct {
	Body         io.ReadCloser
	Size         int64
	LastModified time.Time
}
type Store interface {
	Get(context.Context, string) (Object, error)
	Put(context.Context, string, io.ReadSeeker, int64, string) error
}
type S3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}
type S3Store struct {
	Client                              S3API
	Bucket, ExpectedBucketOwner, Prefix string
}

func storageError(err error) error {
	// Return the sentinel itself so wrapped SDK or provider messages cannot leak.
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrCredentialsExpired, ErrCredentialsInvalid, ErrCredentialsUnavailable, ErrUnavailable, ErrDenied} {
		if errors.Is(err, known) {
			return known
		}
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "ExpiredToken", "ExpiredTokenException", "RequestExpired", "TokenRefreshRequired":
			return ErrCredentialsExpired
		case "InvalidClientTokenId", "UnrecognizedClientException", "SignatureDoesNotMatch", "InvalidAccessKeyId", "InvalidToken", "AuthFailure", "RequestTimeTooSkewed":
			return ErrCredentialsInvalid
		case "NoSuchKey", "NotFound":
			return ErrNotFound
		case "AccessDenied", "AccessDeniedException":
			return ErrDenied
		case "PreconditionFailed", "ConditionalRequestConflict":
			return ErrConflict
		}
	}
	return ErrUnavailable
}

// Credential retrieval in the SDK is wrapped with ordinary errors rather than
// a typed credential failure. Classify at the provider boundary, without parsing
// its text, and retain the same underlying provider/cache for every operation.
type storageCredentials struct{ aws.CredentialsProvider }

// SanitizedCredentials preserves the shared provider/cache while separating
// local credential failures from retryable service/transport errors. The returned
// provider never exposes raw helper output through its errors. Resource clients
// can apply it per operation so wrapped SDK errors retain these sentinel values.
func SanitizedCredentials(provider aws.CredentialsProvider) aws.CredentialsProvider {
	if provider == nil {
		return nil
	}
	return storageCredentials{provider}
}

func (p storageCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	credentials, err := p.CredentialsProvider.Retrieve(ctx)
	if err == nil {
		return credentials, nil
	}
	if ctx.Err() != nil {
		return aws.Credentials{}, ctx.Err()
	}
	classified := storageError(err)
	if errors.Is(classified, ErrNotFound) || errors.Is(classified, ErrConflict) || errors.Is(classified, ErrDenied) {
		// An upstream credential source cannot establish S3 object state or
		// authorize the missing-object fallback, including on AccessDenied.
		return aws.Credentials{}, ErrCredentialsUnavailable
	}
	if !errors.Is(classified, ErrUnavailable) {
		return aws.Credentials{}, classified
	}
	var api smithy.APIError
	var connection interface{ ConnectionError() bool }
	var network net.Error
	if errors.As(err, &api) {
		if api.ErrorFault() == smithy.FaultServer {
			return aws.Credentials{}, ErrUnavailable
		}
		switch api.ErrorCode() {
		case "Throttling", "ThrottlingException", "TooManyRequestsException", "RequestLimitExceeded", "SlowDown", "SlowDownException", "InternalServerError", "InternalServerException", "InternalFailure", "ServiceUnavailable", "ServiceUnavailableException", "RequestTimeout", "RequestTimeoutException":
			return aws.Credentials{}, ErrUnavailable
		default:
			// Invalid SSO access/refresh tokens and other client-side source
			// failures require authentication/configuration repair, not polling.
			return aws.Credentials{}, ErrCredentialsUnavailable
		}
	}
	if errors.Is(err, ErrUnavailable) || errors.Is(err, io.ErrUnexpectedEOF) || (errors.As(err, &connection) && connection.ConnectionError()) || errors.As(err, &network) {
		return aws.Credentials{}, ErrUnavailable
	}
	return aws.Credentials{}, ErrCredentialsUnavailable
}

func withStorageCredentials(o *s3.Options) {
	o.Credentials = SanitizedCredentials(o.Credentials)
}
func (s S3Store) allowed(key string) bool {
	if s.Client == nil || s.Bucket == "" || !accountPattern.MatchString(s.ExpectedBucketOwner) || !strings.HasPrefix(s.Prefix, "results/v1/") || !strings.HasSuffix(s.Prefix, "/") || !strings.HasPrefix(key, s.Prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(key, s.Prefix), "/")
	if len(parts) != 2 || !ValidCommandID(parts[0]) {
		return false
	}
	switch parts[1] {
	case "request.json", "acknowledgement.json", "started.json", "outcome.json", "result.json", "stdout", "stderr":
		return true
	}
	return false
}
func (s S3Store) Get(ctx context.Context, key string) (Object, error) {
	if !s.allowed(key) {
		return Object{}, ErrCorrupt
	}
	obj, err := s.get(ctx, key)
	if !errors.Is(err, ErrDenied) {
		return obj, err
	}
	// Prefix-scoped ListBucket may not authorize the no-prefix access check
	// inside GetObject, making an absent key look denied. A bounded authenticated
	// listing can establish exact-key absence, but never successful retrieval.
	listed, listErr := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.Bucket), ExpectedBucketOwner: aws.String(s.ExpectedBucketOwner), Prefix: aws.String(key), MaxKeys: aws.Int32(1)}, withStorageCredentials)
	if listErr != nil {
		return Object{}, storageError(listErr)
	}
	if listed != nil && aws.ToString(listed.Name) == s.Bucket && aws.ToString(listed.Prefix) == key && len(listed.Contents) <= 1 {
		if len(listed.Contents) == 0 && !aws.ToBool(listed.IsTruncated) {
			return Object{}, ErrNotFound
		}
		if len(listed.Contents) == 1 {
			found := aws.ToString(listed.Contents[0].Key)
			if found == key {
				// Publication may have occurred between the denied GET and LIST.
				// Retry the exact GET once; an extant object can still be denied.
				return s.get(ctx, key)
			}
			if strings.HasPrefix(found, key) {
				return Object{}, ErrNotFound
			}
		}
	}
	return Object{}, ErrDenied
}

func (s S3Store) get(ctx context.Context, key string) (Object, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), ExpectedBucketOwner: aws.String(s.ExpectedBucketOwner)}, withStorageCredentials)
	if err != nil {
		return Object{}, storageError(err)
	}
	if out == nil || out.Body == nil || out.ContentLength == nil || *out.ContentLength < 0 || out.LastModified == nil {
		if out != nil && out.Body != nil {
			_ = out.Body.Close()
		}
		return Object{}, ErrCorrupt
	}
	return Object{out.Body, *out.ContentLength, *out.LastModified}, nil
}

// Put always uses one wire attempt. In particular, a start claim must never
// turn an uncertain response into authorization to execute on a later retry.
func (s S3Store) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, digest string) error {
	if !s.allowed(key) || size < 0 || size > MaxStreamBytes || !ValidSHA256(digest) {
		return ErrCorrupt
	}
	sum, _ := hex.DecodeString(digest)
	out, err := s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), ExpectedBucketOwner: aws.String(s.ExpectedBucketOwner), IfNoneMatch: aws.String("*"), ServerSideEncryption: s3types.ServerSideEncryptionAes256, ContentLength: aws.Int64(size), Body: body, ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum))}, withStorageCredentials, func(o *s3.Options) { o.RetryMaxAttempts = 1; o.Retryer = aws.NopRetryer{} })
	if err != nil {
		return storageError(err)
	}
	if out == nil {
		return ErrUnavailable
	}
	return nil
}
func Digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// PutImmutable reconciles an uncertain immutable data/metadata write by reading
// all bytes and verifying length and SHA-256 under the caller's deadline. Never
// use this helper to acquire started.json: only its initial PUT may authorize a
// launch. A denied read cannot prove absence or successful publication.
func PutImmutable(ctx context.Context, s Store, key string, body io.ReadSeeker, size int64, digest string) error {
	if err := s.Put(ctx, key, body, size, digest); err == nil {
		return nil
	}
	obj, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	defer obj.Body.Close()
	if obj.Size != size {
		return ErrCorrupt
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(obj.Body, size+1))
	if err != nil {
		return bodyError(ctx, err)
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return ErrCorrupt
	}
	return nil
}
func ReadRecord(ctx context.Context, s Store, key string) (Record, time.Time, error) {
	obj, err := s.Get(ctx, key)
	if err != nil {
		return Record{}, time.Time{}, err
	}
	defer obj.Body.Close()
	if obj.Size > MaxRecordBytes {
		return Record{}, time.Time{}, ErrCorrupt
	}
	b, err := io.ReadAll(io.LimitReader(obj.Body, MaxRecordBytes+1))
	if err != nil {
		return Record{}, time.Time{}, bodyError(ctx, err)
	}
	if int64(len(b)) != obj.Size || len(b) > MaxRecordBytes {
		return Record{}, time.Time{}, ErrCorrupt
	}
	r, err := DecodeRecord(b)
	if err != nil {
		if errors.Is(err, ErrUnsupportedSchema) {
			return Record{}, time.Time{}, err
		}
		return Record{}, time.Time{}, ErrCorrupt
	}
	if ObjectKey(r.Scope, r.CommandID, r.Kind+".json") != key {
		return Record{}, time.Time{}, ErrCorrupt
	}
	return r, obj.LastModified, nil
}

func bodyError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, canceled := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, canceled) {
			return canceled
		}
	}
	return ErrUnavailable
}
