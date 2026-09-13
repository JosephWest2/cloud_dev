package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const logsTestID = "dc1-0123456789abcdef0123456789abcdef"

type forbiddenSSM struct{ t *testing.T }

func (s forbiddenSSM) GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	s.t.Fatal("completed retrieval required unavailable SSM history")
	return nil, errors.New("SSM unavailable")
}

func storageConfig(t *testing.T) (string, config.Config, config.Results) {
	t.Helper()
	path := testutil.Setup(t)
	testutil.Write(t, path, testutil.Config+"profile_file='missing-profile'\nssh_identity_file='missing-key'\n")
	c, err := config.Load(path, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(testutil.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	for key := range manifest {
		switch key {
		case "schema_version", "account", "region", "deployment", "owner", "results":
		default:
			delete(manifest, key)
		}
	}
	b, _ := json.Marshal(manifest)
	testutil.Write(t, c.Manifest, string(b))
	storage, err := config.LoadResultManifest(c.Manifest, c)
	if err != nil {
		t.Fatal(err)
	}
	return path, c, storage
}

func logsRecord(c config.Config, stdout, stderr []byte, code int) execprotocol.Record {
	scope := execprotocol.Scope{Account: c.ExpectedAccount, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner}
	now := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	stream := func(name string, data []byte) execprotocol.Stream {
		return execprotocol.Stream{Key: execprotocol.ObjectKey(scope, logsTestID, name), Bytes: int64(len(data)), SHA256: execprotocol.Digest(data), Upload: "complete"}
	}
	return execprotocol.Record{
		SchemaVersion: 1, Kind: "result",
		Binding:      execprotocol.Binding{CommandID: logsTestID, Scope: scope, InstanceID: "i-0123456789abcdef0", Document: execprotocol.ExecutionDocument{Name: "deleted-old-document", Version: "17", ContentSHA256: strings.Repeat("a", 64), Step: "execute"}, RunnerSHA256: strings.Repeat("b", 64), PayloadSHA256: strings.Repeat("c", 64)},
		SSMCommandID: "01234567-89ab-cdef-0123-456789abcdef", SubmittedAt: execprotocol.Timestamp(now), ExpiresAt: execprotocol.Timestamp(now.Add(30 * 24 * time.Hour)), FinishedAt: execprotocol.Timestamp(now), FinalizedAt: execprotocol.Timestamp(now),
		Workload: &execprotocol.Workload{Status: "exited", ExitCode: &code}, Capture: "complete", Publication: "complete", Streams: &execprotocol.Streams{Stdout: stream("stdout", stdout), Stderr: stream("stderr", stderr)},
	}
}

// This combines the actual SDK store, strict cloud lookup and exporter with a
// storage-only descriptor. No local receipt, launch resources or SSM exist.
func TestRunSDKRecoversHistoricalBinaryExportsWithoutRuntimeOrSSM(t *testing.T) {
	path, c, storage := storageConfig(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stdout := bytes.Repeat([]byte{0, 0xff, '\n', 'a'}, 131073)
	stderr := []byte{}
	record := logsRecord(c, stdout, stderr, 255)
	metadata, err := execprotocol.EncodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string][]byte{record.Streams.Stdout.Key: stdout, record.Streams.Stderr.Key: stderr, execprotocol.ObjectKey(record.Scope, logsTestID, "result.json"): metadata}
	var requests []string
	var requestsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("X-Amz-Expected-Bucket-Owner") != c.ExpectedAccount || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("retrieval was not a scoped authenticated GET")
			w.WriteHeader(403)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/"+storage.Bucket+"/")
		requestsMu.Lock()
		requests = append(requests, key)
		requestsMu.Unlock()
		body, ok := objects[key]
		if !ok {
			t.Error("retrieval read an unexpected key")
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client := s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), Retryer: func() aws.Retryer { return aws.NopRetryer{} }}, func(o *s3.Options) {
		o.BaseEndpoint, o.UsePathStyle = aws.String(server.URL), true
	})
	deps := Dependencies{New: func(_ context.Context, got config.Config, result config.Results) (*Service, error) {
		if got.AWSProfile != "selected-operator" || result != storage {
			t.Fatal("selected profile or storage scope lost")
		}
		return &Service{Store: execprotocol.S3Store{Client: client, Bucket: storage.Bucket, ExpectedBucketOwner: c.ExpectedAccount, Prefix: storage.Prefix}, SSM: forbiddenSSM{t}}, nil
	}}
	options := Options{CommandID: logsTestID}
	requestCount := func() int { requestsMu.Lock(); defer requestsMu.Unlock(); return len(requests) }
	r := Run(context.Background(), path, config.Overrides{AWSProfile: "selected-operator"}, options, deps, io.Discard)
	if !r.OK || r.ExitCode != 0 || r.Outcome != "complete" || r.Verification != "not_downloaded" || r.Workload == nil || *r.Workload.ExitCode != 255 || requestCount() != 1 {
		t.Fatalf("status-only result: %+v requests=%d", r, requestCount())
	}
	options.StdoutFile, options.StderrFile = filepath.Join(t.TempDir(), "stdout.bin"), filepath.Join(t.TempDir(), "stderr.bin")
	r = Run(context.Background(), path, config.Overrides{AWSProfile: "selected-operator"}, options, deps, io.Discard)
	if !r.OK || r.ExitCode != 0 || r.Verification != "verified" || r.Encoding != "bytes" || *r.Workload.ExitCode != 255 || r.Downloads == nil || r.Downloads.Stdout.File != options.StdoutFile || r.Downloads.Stderr.File != options.StderrFile || requestCount() != 4 {
		t.Fatalf("verified exports: %+v requests=%d", r, requestCount())
	}
	for file, want := range map[string][]byte{options.StdoutFile: stdout, options.StderrFile: stderr} {
		got, err := os.ReadFile(file)
		info, statErr := os.Stat(file)
		if err != nil || statErr != nil || !bytes.Equal(got, want) || info.Mode().Perm() != 0600 {
			t.Fatalf("export differs or is not private: %s", file)
		}
	}
	if !strings.Contains(r.RecoveryCommand, "selected-operator") || !strings.Contains(r.RecoveryCommand, path) {
		t.Fatal("recovery command lost original scope")
	}
}

type runStore struct {
	metadata []byte
	stdout   []byte
	fail     error
	get      func(context.Context, string) (execprotocol.Object, error)
}

func (s runStore) Get(ctx context.Context, key string) (execprotocol.Object, error) {
	if s.get != nil {
		return s.get(ctx, key)
	}
	data := s.metadata
	if strings.HasSuffix(key, "/stdout") {
		if s.fail != nil {
			return execprotocol.Object{}, s.fail
		}
		data = s.stdout
	} else if strings.HasSuffix(key, "/stderr") {
		data = nil
	}
	return execprotocol.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data)), LastModified: time.Now()}, nil
}
func (runStore) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	panic("logs must never write result storage")
}

func TestRunOutputFailurePreservesKnownWorkload(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		err           error
		data          []byte
	}{
		{"denied", "access_denied", execprotocol.ErrDenied, nil},
		{"missing", "incomplete", execprotocol.ErrNotFound, nil},
		{"corrupt", "corrupt", nil, []byte("wrong")},
		{"credentials", "unavailable", execprotocol.ErrCredentialsExpired, nil},
		{"opaque", "unavailable", errors.New("SECRET provider or URL"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, c, _ := storageConfig(t)
			record := logsRecord(c, []byte("right"), nil, 4)
			metadata, _ := execprotocol.EncodeRecord(record)
			store := runStore{metadata: metadata, stdout: tc.data, fail: tc.err}
			var out bytes.Buffer
			r := Run(context.Background(), path, config.Overrides{}, Options{CommandID: logsTestID, Stream: "stdout"}, Dependencies{New: func(context.Context, config.Config, config.Results) (*Service, error) {
				return &Service{Store: store, SSM: forbiddenSSM{t}}, nil
			}}, &out)
			data, _ := json.Marshal(r)
			if r.OK || r.ExitCode != 1 || r.Outcome != tc.outcome || r.Workload == nil || *r.Workload.ExitCode != 4 || r.Publication != "complete" || r.Verification != "failed" || r.CommandID != logsTestID || r.RecoveryCommand == "" || strings.Contains(string(data), "SECRET") {
				t.Fatalf("retrieval failure lost trusted status: %s", data)
			}
		})
	}
}

func TestRunSecondExportFailureReportsFirstVerifiedFile(t *testing.T) {
	path, c, _ := storageConfig(t)
	record := logsRecord(c, []byte("verified first output"), nil, 2)
	metadata, _ := execprotocol.EncodeRecord(record)
	baseStore := runStore{metadata: metadata, stdout: []byte("verified first output")}
	store := runStore{get: func(ctx context.Context, key string) (execprotocol.Object, error) {
		if strings.HasSuffix(key, "/stderr") {
			return execprotocol.Object{}, execprotocol.ErrDenied
		}
		return baseStore.Get(ctx, key)
	}}
	dir := t.TempDir()
	options := Options{CommandID: logsTestID, StdoutFile: filepath.Join(dir, "first.bin"), StderrFile: filepath.Join(dir, "second.bin")}
	r := Run(context.Background(), path, config.Overrides{}, options, Dependencies{New: func(context.Context, config.Config, config.Results) (*Service, error) {
		return &Service{Store: store}, nil
	}}, io.Discard)
	first, err := os.ReadFile(options.StdoutFile)
	_, secondErr := os.Lstat(options.StderrFile)
	entries, dirErr := os.ReadDir(dir)
	if r.ExitCode != 1 || r.OK || r.Outcome != "access_denied" || r.Verification != "failed" || r.Downloads.Stdout.Verification != "verified" || r.Downloads.Stdout.File != options.StdoutFile || r.Downloads.Stderr.File != "" || r.Workload == nil || *r.Workload.ExitCode != 2 || err != nil || string(first) != "verified first output" || !errors.Is(secondErr, os.ErrNotExist) || dirErr != nil || len(entries) != 1 {
		t.Fatalf("partial export evidence or cleanup differs: %+v", r)
	}
}

func TestRunCompleteTimeoutResultIsSuccessfulRetrieval(t *testing.T) {
	path, c, _ := storageConfig(t)
	record := logsRecord(c, nil, nil, 0)
	signal := 15
	record.Workload = &execprotocol.Workload{Status: "execution_timeout", Signal: &signal}
	metadata, err := execprotocol.EncodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	r := Run(context.Background(), path, config.Overrides{}, Options{CommandID: logsTestID, Stream: "stderr"}, Dependencies{New: func(context.Context, config.Config, config.Results) (*Service, error) {
		return &Service{Store: runStore{metadata: metadata}, SSM: forbiddenSSM{t}}, nil
	}}, io.Discard)
	if !r.OK || r.ExitCode != 0 || r.Outcome != "complete" || r.Workload == nil || r.Workload.Status != "execution_timeout" || r.Workload.ExitCode != nil || r.Verification != "verified" || r.Downloads.Stderr.BytesWritten != 0 {
		t.Fatalf("workload timeout became retrieval timeout: %+v", r)
	}
}

func TestRunRejectsUsageAndWrongStorageScopeBeforeAWS(t *testing.T) {
	deps := Dependencies{New: func(context.Context, config.Config, config.Results) (*Service, error) {
		t.Fatal("invalid input reached AWS")
		return nil, nil
	}}
	for _, options := range []Options{
		{CommandID: "https://SECRET/key"}, {CommandID: "dc1-../../SECRET"},
		{CommandID: logsTestID, Stream: "both"}, {CommandID: logsTestID, Stream: "stdout", StdoutFile: "new"},
		{CommandID: logsTestID, Timeout: 6 * time.Minute},
	} {
		r := Run(context.Background(), "/not-read", config.Overrides{}, options, deps, io.Discard)
		b, _ := json.Marshal(r)
		if r.ExitCode != 2 || strings.Contains(string(b), "SECRET") {
			t.Fatalf("unsafe input result: %s", b)
		}
	}
	path, c, _ := storageConfig(t)
	data, _ := os.ReadFile(c.Manifest)
	testutil.Write(t, c.Manifest, strings.Replace(string(data), c.ExpectedAccount, "000000000000", 1))
	r := Run(context.Background(), path, config.Overrides{}, Options{CommandID: logsTestID}, deps, io.Discard)
	if r.ExitCode != 2 || r.Code != "result_manifest_invalid" {
		t.Fatalf("wrong-scope manifest result: %+v", r)
	}
}

func TestRunIdentityAndReadDeadlinesRemainLocal(t *testing.T) {
	path, _, _ := storageConfig(t)
	for _, identityPhase := range []bool{false, true} {
		r := Run(context.Background(), path, config.Overrides{}, Options{CommandID: logsTestID, Timeout: 10 * time.Millisecond}, Dependencies{New: func(ctx context.Context, _ config.Config, _ config.Results) (*Service, error) {
			if identityPhase {
				<-ctx.Done()
				return nil, &identity.Failure{Code: "timeout", Message: "identity canceled"}
			}
			return &Service{Store: runStore{get: func(ctx context.Context, _ string) (execprotocol.Object, error) {
				<-ctx.Done()
				return execprotocol.Object{}, ctx.Err()
			}}}, nil
		}}, io.Discard)
		if r.ExitCode != 4 || r.Outcome != "timeout" || r.Workload != nil || r.CommandID != logsTestID {
			t.Fatalf("local deadline fabricated workload timeout: %+v", r)
		}
	}
}
