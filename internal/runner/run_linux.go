package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/logging"
)

const ConfigPath = "/etc/devbox/execution.json"
const ExecutablePath = "/usr/local/libexec/devbox-runner"
const CaptureDirectory = "/var/lib/devbox/commands"

type WorkerConfig struct {
	SchemaVersion int              `json:"schema_version"`
	Account       string           `json:"account"`
	Region        string           `json:"region"`
	Deployment    string           `json:"deployment"`
	Owner         string           `json:"owner"`
	Execution     config.Execution `json:"execution"`
	Results       config.Results   `json:"results"`
}

func (c WorkerConfig) Scope() execprotocol.Scope {
	return execprotocol.Scope{Account: c.Account, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner}
}
func (c WorkerConfig) Validate() error {
	user := config.Config{ExpectedAccount: c.Account, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner}
	if c.SchemaVersion != 1 || c.Scope().Validate() != nil || config.ValidateExecution(c.Execution) != nil || config.ValidateResults(c.Results, user) != nil {
		return errors.New("invalid worker configuration")
	}
	return nil
}

type Input struct{ CommandID, SSMCommandID, EncodedPayload string }
type Identity struct{ Account, Region, InstanceID string }
type Dependencies struct {
	Store               execprotocol.Store
	Identity            func(context.Context) (Identity, error)
	SelfSHA256          func() (string, error)
	Execute             func(context.Context, context.Context, execprotocol.Payload) Captured
	Now                 func() time.Time
	PublicationTimeout  time.Duration // Zero selects the production two-minute bound.
	PreparationDeadline time.Time     // Production includes configuration/user setup in the 30s budget.
}
type Result struct {
	Code     string
	ExitCode int
	Record   *execprotocol.Record
}

// Run returns a publication status, never the workload's numeric exit code.
// Context cancellation stops the workload; final publication uses a separate
// bounded context so wrapper termination can preserve an honest partial outcome.
func Run(ctx context.Context, c WorkerConfig, in Input, d Dependencies) Result {
	fail := func(code string) Result { return Result{Code: code, ExitCode: 1} }
	if c.Validate() != nil || !execprotocol.ValidCommandID(in.CommandID) || !execprotocol.ValidSSMCommandID(in.SSMCommandID) {
		return fail("runner_config_invalid")
	}
	p, err := execprotocol.DecodePayload(in.EncodedPayload)
	if err != nil {
		return fail("runner_payload_invalid")
	}
	if d.Store == nil || d.Identity == nil || d.SelfSHA256 == nil || d.Execute == nil {
		return fail("runner_dependencies_invalid")
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	hard, hardCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(p.ExecTimeoutSeconds)*time.Second+180*time.Second)
	defer hardCancel()
	prepareDeadline := time.Now().Add(30 * time.Second)
	if !d.PreparationDeadline.IsZero() && d.PreparationDeadline.Before(prepareDeadline) {
		prepareDeadline = d.PreparationDeadline
	}
	prepare, cancel := context.WithDeadline(ctx, prepareDeadline)
	defer cancel()
	id, err := d.Identity(prepare)
	if err != nil || id.Account != c.Account || id.Region != c.Region {
		return fail("runner_identity_failed")
	}
	hash, err := d.SelfSHA256()
	if err != nil || hash != c.Execution.RunnerSHA256 {
		return fail("runner_artifact_changed")
	}
	decoded, _ := base64.StdEncoding.DecodeString(in.EncodedPayload)
	binding := execprotocol.Binding{CommandID: in.CommandID, Scope: c.Scope(), InstanceID: id.InstanceID, Document: execprotocol.ExecutionDocument{Name: c.Execution.Name, Version: c.Execution.Version, ContentSHA256: c.Execution.ContentSHA256, Step: c.Execution.Step}, RunnerSHA256: hash, PayloadSHA256: execprotocol.Digest(decoded)}
	if binding.Validate() != nil {
		return fail("runner_identity_failed")
	}
	key := func(name string) string { return execprotocol.ObjectKey(c.Scope(), in.CommandID, name) }
	request, submitted, err := execprotocol.ReadRecord(prepare, d.Store, key("request.json"))
	if err != nil || request.Kind != "request" || request.Binding != binding || request.RetentionDays != c.Results.RetentionDays || submitted.IsZero() {
		return fail("runner_request_invalid")
	}
	// S3 server time is authoritative. Copy its exact whole-second creation time
	// into every later record rather than accepting a client supplied timestamp.
	submitted = submitted.UTC().Truncate(time.Second)
	expires := submitted.Add(time.Duration(request.RetentionDays) * 24 * time.Hour)
	if !now().Before(expires) || now().Before(submitted) {
		return fail("runner_request_expired")
	}
	started := execprotocol.Record{SchemaVersion: 1, Kind: "started", Binding: binding, SSMCommandID: in.SSMCommandID, SubmittedAt: execprotocol.Timestamp(submitted), ExpiresAt: execprotocol.Timestamp(expires), StartedAt: execprotocol.Timestamp(now())}
	b, err := execprotocol.EncodeRecord(started)
	if err != nil || prepare.Err() != nil {
		return fail("runner_preparation_timeout")
	}
	// A conflict or lost response aborts. No read reconciliation or retry may
	// convert this uncertain claim into permission to start the workload.
	if err = d.Store.Put(prepare, key("started.json"), bytes.NewReader(b), int64(len(b)), execprotocol.Digest(b)); err != nil {
		return fail("runner_claim_unconfirmed")
	}
	if prepare.Err() != nil {
		return fail("runner_preparation_timeout")
	}
	execution, stop := context.WithCancel(hard)
	stopOnSignal := context.AfterFunc(ctx, stop)
	captured := d.Execute(execution, prepare, p)
	stopOnSignal()
	stop()
	cancel()
	if captured.Remove != nil {
		defer captured.Remove()
	}
	publication := d.PublicationTimeout
	if publication <= 0 || publication > 120*time.Second {
		publication = 120 * time.Second
	}
	pub, pubCancel := context.WithTimeout(hard, publication)
	defer pubCancel()
	record := started
	record.Kind = "outcome"
	record.StartedAt = ""
	record.FinishedAt = execprotocol.Timestamp(captured.FinishedAt)
	record.Workload = &captured.Workload
	record.Capture = captured.Capture
	stdout, outErr := fileStream(captured.Stdout, key("stdout"))
	stderr, errErr := fileStream(captured.Stderr, key("stderr"))
	if outErr != nil || errErr != nil {
		record.Capture = "incomplete"
	}
	record.Streams = &execprotocol.Streams{Stdout: stdout, Stderr: stderr}
	writeRecord := func(r execprotocol.Record) error {
		data, err := execprotocol.EncodeRecord(r)
		if err != nil {
			return err
		}
		return execprotocol.PutImmutable(pub, d.Store, key(r.Kind+".json"), bytes.NewReader(data), int64(len(data)), execprotocol.Digest(data))
	}
	// Preserve the workload outcome first. Upload failures do not erase it.
	if err = writeRecord(record); err != nil {
		return Result{Code: "runner_outcome_unavailable", ExitCode: 1, Record: &record}
	}
	upload := func(path string, s *execprotocol.Stream, prior error) {
		if prior != nil {
			s.Upload = "failed"
			return
		}
		file, err := os.Open(path)
		if err == nil {
			defer file.Close()
			err = execprotocol.PutImmutable(pub, d.Store, s.Key, file, s.Bytes, s.SHA256)
		}
		if err != nil {
			s.Upload = "failed"
		} else {
			s.Upload = "complete"
		}
	}
	upload(captured.Stdout, &record.Streams.Stdout, outErr)
	upload(captured.Stderr, &record.Streams.Stderr, errErr)
	record.Kind = "result"
	record.FinalizedAt = execprotocol.Timestamp(now())
	record.Publication = "incomplete"
	if record.Workload.Status != "unknown" && record.Capture == "complete" && record.Streams.Stdout.Upload == "complete" && record.Streams.Stderr.Upload == "complete" {
		record.Publication = "complete"
	}
	if err = writeRecord(record); err != nil {
		return Result{Code: "runner_result_unavailable", ExitCode: 1, Record: &record}
	}
	if record.Publication != "complete" {
		return Result{Code: "runner_result_incomplete", ExitCode: 1, Record: &record}
	}
	return Result{Code: "runner_result_published", Record: &record}
}

func fileStream(path, key string) (execprotocol.Stream, error) {
	s := execprotocol.Stream{Key: key, SHA256: execprotocol.Digest(nil), Upload: "pending"}
	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, execprotocol.MaxStreamBytes+1))
	if n > execprotocol.MaxStreamBytes {
		return s, errors.New("capture exceeds stream limit")
	}
	s.Bytes = n
	s.SHA256 = hex.EncodeToString(h.Sum(nil))
	return s, err
}

func LoadConfig(path string) (WorkerConfig, error) {
	var c WorkerConfig
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("worker configuration unavailable")
	}
	defer f.Close()
	if !rootOwned(f) {
		return c, errors.New("worker configuration is not root owned and protected")
	}
	b, err := io.ReadAll(io.LimitReader(f, execprotocol.MaxRecordBytes+1))
	if err != nil || len(b) > execprotocol.MaxRecordBytes || execprotocol.StrictJSON(b, &c) != nil || c.Validate() != nil {
		return c, errors.New("invalid worker configuration")
	}
	return c, nil
}
func rootOwned(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&022 != 0 {
		return false
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
func selfDigest() (string, error) {
	// /proc/self/exe binds the running inode, including after a path replacement.
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if !rootOwned(f) {
		return "", errors.New("runner artifact permissions differ")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ProductionDependencies(c WorkerConfig) (Dependencies, error) {
	if os.Geteuid() != 0 {
		return Dependencies{}, errors.New("runner requires root")
	}
	u, err := user.Lookup("devbox")
	if err != nil {
		return Dependencies{}, errors.New("development user unavailable")
	}
	uid, e1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, e2 := strconv.ParseUint(u.Gid, 10, 32)
	if e1 != nil || e2 != nil || uid == 0 || u.HomeDir != "/home/devbox" {
		return Dependencies{}, errors.New("invalid development user")
	}
	groups, err := u.GroupIds()
	if err != nil {
		return Dependencies{}, errors.New("development groups unavailable")
	}
	credential := &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	for _, g := range groups {
		n, err := strconv.ParseUint(g, 10, 32)
		if err != nil {
			return Dependencies{}, errors.New("invalid development group")
		}
		credential.Groups = append(credential.Groups, uint32(n))
	}
	if err := os.MkdirAll(CaptureDirectory, 0700); err != nil {
		return Dependencies{}, errors.New("capture directory unavailable")
	}
	st, err := os.Lstat(CaptureDirectory)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return Dependencies{}, errors.New("capture directory permissions differ")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return Dependencies{}, errors.New("capture directory owner differs")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	httpClient := &http.Client{Transport: transport}
	metadata := imds.New(imds.Options{Endpoint: "http://169.254.169.254", HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Second}, EnableFallback: aws.FalseTernary, ClientEnableState: imds.ClientEnabled, Logger: logging.Nop{}})
	awsConfig := aws.Config{Region: c.Region, Credentials: aws.NewCredentialsCache(ec2rolecreds.New(func(o *ec2rolecreds.Options) { o.Client = metadata })), HTTPClient: httpClient, Logger: logging.Nop{}, Retryer: func() aws.Retryer { return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 2 }) }}
	store := execprotocol.S3Store{Client: s3.NewFromConfig(awsConfig), Bucket: c.Results.Bucket, ExpectedBucketOwner: c.Results.ExpectedBucketOwner, Prefix: c.Results.Prefix}
	executor := Executor{HelperPath: ExecutablePath, Directory: CaptureDirectory, Credential: credential}
	return Dependencies{Store: store, SelfSHA256: selfDigest, Execute: executor.Execute, Identity: func(ctx context.Context) (Identity, error) {
		out, err := metadata.GetInstanceIdentityDocument(ctx, &imds.GetInstanceIdentityDocumentInput{})
		if err != nil || out == nil {
			return Identity{}, errors.New("IMDSv2 identity unavailable")
		}
		return Identity{out.AccountID, out.Region, out.InstanceID}, nil
	}}, nil
}
