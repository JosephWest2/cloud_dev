// Package execution submits literal commands to one verified managed worker.
package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/foundation"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

type Targets interface {
	Resolve(context.Context, string) ([]lifecycle.Instance, error)
	WaitReady(context.Context, config.Manifest, *lifecycle.Instance, io.Writer) error
}

type SSM interface {
	GetDocument(context.Context, *ssm.GetDocumentInput, ...func(*ssm.Options)) (*ssm.GetDocumentOutput, error)
	DescribeInstanceInformation(context.Context, *ssm.DescribeInstanceInformationInput, ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
	SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

type Service struct {
	Scope    config.Config
	Manifest config.Manifest
	Targets  Targets
	SSM      SSM
	Store    execprotocol.Store
	Storage  foundation.S3
	// CheckStorage is independently replaceable in tests; production always
	// verifies the bucket's scope, privacy, encryption and retention safeguards.
	CheckStorage func(context.Context) error
}

type Submission struct {
	Binding               execprotocol.Binding `json:"binding"`
	SSMCommandID          string               `json:"ssm_command_id,omitempty"`
	State                 string               `json:"state"`
	AcknowledgementStored bool                 `json:"acknowledgement_stored"`
	DiagnosticCode        string               `json:"diagnostic_code,omitempty"`
}

type Failure struct{ Code, Message string }

func (f *Failure) Error() string         { return f.Message }
func failure(code, message string) error { return &Failure{code, message} }

// New verifies STS before creating resource clients. The same SDK configuration
// and credential cache serve readiness, storage, and dispatch; no SSH or launch
// profile prerequisite is involved.
func New(ctx context.Context, c config.Config, m config.Manifest) (*Service, error) {
	a, err := identity.Load(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := identity.Verify(ctx, sts.NewFromConfig(a), c.ExpectedAccount); err != nil {
		return nil, err
	}
	compute, commands, objects := ec2.NewFromConfig(a), ssm.NewFromConfig(a), s3.NewFromConfig(a)
	s := &Service{Scope: c, Manifest: m, Targets: &lifecycle.Service{API: compute, SSM: commands, Scope: c}, SSM: commands, Storage: objects,
		Store: execprotocol.S3Store{Client: objects, Bucket: m.Results.Bucket, ExpectedBucketOwner: m.Results.ExpectedBucketOwner, Prefix: m.Results.Prefix}}
	s.CheckStorage = func(ctx context.Context) error {
		return foundation.CheckResults(ctx, objects, m.Results, c.Deployment, c.Owner)
	}
	return s, nil
}

// Prepare performs all argument and execution-clock validation without network
// access. POSIX path rules describe the remote filesystem on every local OS.
func Prepare(argv []string, cwd string, executionTimeout time.Duration) (execprotocol.Payload, error) {
	if executionTimeout < time.Second || executionTimeout > 24*time.Hour || executionTimeout%time.Second != 0 {
		return execprotocol.Payload{}, failure("exec_timeout_invalid", "execution timeout must be whole seconds from 1s through 24h")
	}
	if !utf8.ValidString(cwd) || strings.ContainsRune(cwd, 0) {
		return execprotocol.Payload{}, failure("payload_invalid", "execution arguments and directory must be valid UTF-8 without NUL")
	}
	if !path.IsAbs(cwd) {
		cwd = path.Join(execprotocol.DefaultCwd, cwd)
	} else {
		cwd = path.Clean(cwd)
	}
	p, err := execprotocol.NewPayload(argv, cwd, int(executionTimeout/time.Second))
	if err != nil {
		return execprotocol.Payload{}, failure("payload_invalid", err.Error())
	}
	if _, err := execprotocol.EncodePayload(p); err != nil {
		return execprotocol.Payload{}, failure("payload_invalid", err.Error())
	}
	return p, nil
}

func (s *Service) target(ctx context.Context, name string) (lifecycle.Instance, error) {
	instances, err := s.Targets.Resolve(ctx, name)
	if err != nil {
		return lifecycle.Instance{}, err
	}
	if len(instances) != 1 {
		return lifecycle.Instance{}, failure("target_unresolved", "execution requires exactly one managed target")
	}
	i := instances[0]
	image := s.Manifest.Images["agent"]
	if i.TemplateID != image.LaunchTemplateID || i.TemplateVersion != image.LaunchTemplateVersion || i.Image != image.AMIID || image.LaunchTemplateID == "" || image.LaunchTemplateVersion == "" {
		return i, failure("execution_target_stale", "target does not use the exported execution foundation; launch a worker from the current foundation")
	}
	if i.State != "running" && i.State != "pending" {
		return i, failure("instance_not_running", "target must be running before execution; inspect or remove the instance by ID")
	}
	return i, nil
}

func (s *Service) checkRuntime(ctx context.Context) error {
	e := s.Manifest.Execution
	doc, err := s.SSM.GetDocument(ctx, &ssm.GetDocumentInput{Name: aws.String(e.Name), DocumentVersion: aws.String(e.Version), DocumentFormat: ssmtypes.DocumentFormatJson})
	if err != nil {
		return preflightError(ctx, "execution_document_unavailable", "cannot read the pinned execution document; check permissions and re-export the foundation")
	}
	if doc == nil || aws.ToString(doc.Name) != e.Name || aws.ToString(doc.DocumentVersion) != e.Version || doc.DocumentType != ssmtypes.DocumentTypeCommand || doc.Status != ssmtypes.DocumentStatusActive {
		return failure("execution_document_changed", "pinned execution document is unavailable or changed; review and re-export the foundation")
	}
	var value any
	if json.Unmarshal([]byte(aws.ToString(doc.Content)), &value) != nil {
		return failure("execution_document_changed", "pinned execution document has invalid content")
	}
	canonical, _ := json.Marshal(value)
	if execprotocol.Digest(canonical) != e.ContentSHA256 {
		return failure("execution_document_changed", "execution document differs from its trusted hash; review and re-export the foundation")
	}
	obj, err := s.Storage.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Manifest.Results.Bucket), ExpectedBucketOwner: aws.String(s.Manifest.Results.ExpectedBucketOwner), Key: aws.String("artifacts/runner/" + e.RunnerSHA256 + "/linux-amd64")})
	if err != nil {
		return preflightError(ctx, "execution_runner_unavailable", "cannot verify the pinned runner artifact; check storage access and the foundation")
	}
	if obj == nil || aws.ToInt64(obj.ContentLength) <= 0 || obj.Metadata["sha256"] != e.RunnerSHA256 || obj.ServerSideEncryption != s3types.ServerSideEncryptionAes256 {
		return failure("execution_runner_changed", "runner artifact differs from the trusted export; review and re-export the foundation")
	}
	return nil
}

func preflightError(ctx context.Context, code, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return failure(code, message)
}

func versionParts(value string) ([4]uint64, bool) {
	var version [4]uint64
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return version, false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return version, false
		}
		version[i] = n
	}
	return version, true
}

func agentSupported(current, minimum string) bool {
	a, ok := versionParts(current)
	b, valid := versionParts(minimum)
	if !ok || !valid {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func (s *Service) checkAgent(ctx context.Context, instance string) error {
	out, err := s.SSM.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{Filters: []ssmtypes.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: []string{instance}}}})
	if err != nil {
		return preflightError(ctx, "execution_agent_unavailable", "cannot verify the target's SSM agent; check observation permissions and connectivity")
	}
	if out == nil || aws.ToString(out.NextToken) != "" || len(out.InstanceInformationList) != 1 {
		return failure("execution_agent_unavailable", "target has no unambiguous online SSM agent observation")
	}
	i := out.InstanceInformationList[0]
	if aws.ToString(i.InstanceId) != instance || i.PingStatus != ssmtypes.PingStatusOnline || i.PlatformType != ssmtypes.PlatformTypeLinux || i.ResourceType != ssmtypes.ResourceTypeEc2Instance {
		return failure("execution_agent_unavailable", "target SSM observation does not identify an online Linux EC2 worker")
	}
	if !agentSupported(aws.ToString(i.AgentVersion), s.Manifest.Execution.MinimumAgentVersion) {
		return failure("execution_agent_unsupported", "target SSM agent is too old or has an unknown version; launch a worker from the current foundation")
	}
	return nil
}

func requestRejected(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "AccessDenied", "AccessDeniedException", "InvalidDocument", "InvalidDocumentVersion", "InvalidDocumentHash", "InvalidInstanceId", "InvalidParameters", "UnsupportedPlatformType", "InvalidRole", "InvalidOutputFolder", "InvalidNotificationConfig", "DuplicateInstanceId", "ExpiredToken", "ExpiredTokenException", "InvalidClientTokenId", "UnrecognizedClientException":
		return true
	}
	return false
}

// Submit writes a durable request before one non-idempotent SendCommand. A
// public ID is a recovery address, never authority to retry execution. Optional
// acknowledgement publication cannot invalidate an acknowledged submission.
func (s *Service) Submit(ctx context.Context, target string, payload execprotocol.Payload, deliverySeconds int, progress io.Writer) (sub Submission, err error) {
	encoded, err := execprotocol.EncodePayload(payload)
	if err != nil {
		return sub, failure("payload_invalid", err.Error())
	}
	if !lifecycle.ValidTarget(target) {
		return sub, failure("target_invalid", "use a friendly name or EC2 instance ID")
	}
	if deliverySeconds < 30 || deliverySeconds > 3600 {
		return sub, failure("delivery_timeout_invalid", "delivery timeout must be whole seconds from 30s through 1h")
	}
	if s.Targets == nil || s.SSM == nil || s.Store == nil || s.Storage == nil || s.CheckStorage == nil {
		return sub, failure("execution_unavailable", "execution clients are unavailable; reinstall devbox")
	}
	if err := ctx.Err(); err != nil {
		return sub, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return sub, failure("command_id_unavailable", "cannot generate a secure command recovery ID")
	}
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	sub.Binding = execprotocol.Binding{CommandID: "dc1-" + hex.EncodeToString(random), Scope: execprotocol.Scope{Account: s.Scope.ExpectedAccount, Region: s.Scope.Region, Deployment: s.Scope.Deployment, Owner: s.Scope.Owner}, Document: execprotocol.ExecutionDocument{Name: s.Manifest.Execution.Name, Version: s.Manifest.Execution.Version, ContentSHA256: s.Manifest.Execution.ContentSHA256, Step: s.Manifest.Execution.Step}, RunnerSHA256: s.Manifest.Execution.RunnerSHA256, PayloadSHA256: execprotocol.Digest(decoded)}
	if s.Manifest.Account != sub.Binding.Scope.Account || s.Manifest.Region != sub.Binding.Scope.Region || s.Manifest.Deployment != sub.Binding.Scope.Deployment || s.Manifest.Owner != sub.Binding.Scope.Owner || s.Manifest.Results.ExpectedBucketOwner != sub.Binding.Scope.Account || s.Manifest.Results.Region != sub.Binding.Scope.Region || s.Manifest.Results.Prefix != execprotocol.BasePrefix(sub.Binding.Scope) {
		return sub, failure("scope_mismatch", "execution manifest and selected AWS scope differ; re-export the intended foundation")
	}
	instance, err := s.target(ctx, target)
	if err != nil {
		return sub, err
	}
	sub.Binding.InstanceID = instance.ID
	request := execprotocol.Record{SchemaVersion: 1, Kind: "request", Binding: sub.Binding, RetentionDays: s.Manifest.Results.RetentionDays}
	requestBytes, err := execprotocol.EncodeRecord(request)
	if err != nil {
		return sub, failure("execution_config_invalid", "execution scope, runtime pins or retention are invalid; re-export the current foundation")
	}
	if err := s.CheckStorage(ctx); err != nil {
		return sub, preflightError(ctx, "result_storage_unavailable", "result storage failed scope, access or retention validation; run devbox doctor")
	}
	if err := s.checkRuntime(ctx); err != nil {
		return sub, err
	}
	if err := s.Targets.WaitReady(ctx, s.Manifest, &instance, progress); err != nil {
		return sub, err
	}
	if err := s.checkAgent(ctx, instance.ID); err != nil {
		return sub, err
	}
	key := execprotocol.ObjectKey(sub.Binding.Scope, sub.Binding.CommandID, "request.json")
	if err := execprotocol.PutImmutable(ctx, s.Store, key, bytes.NewReader(requestBytes), int64(len(requestBytes)), execprotocol.Digest(requestBytes)); err != nil {
		return sub, preflightError(ctx, "request_publication_failed", "cannot confirm the durable request was stored; no execution was submitted")
	}
	sub.State = "prepared"
	if progress != nil {
		if _, err := fmt.Fprintf(progress, "devbox: prepared command_id=%s\n", sub.Binding.CommandID); err != nil {
			return sub, failure("recovery_output_failed", "cannot print the prepared recovery ID; no execution was submitted")
		}
	}
	// Storage publication can take time: repeat agent and exact EC2 scope/pin
	// checks immediately before sending, always by the original resolved ID.
	if err := s.checkAgent(ctx, instance.ID); err != nil {
		return sub, err
	}
	fresh, err := s.target(ctx, instance.ID)
	if err != nil {
		return sub, err
	}
	if fresh.ID != sub.Binding.InstanceID || fresh.State != "running" {
		return sub, failure("execution_target_changed", "target changed before submission; no execution was submitted")
	}
	if err := ctx.Err(); err != nil {
		return sub, err
	}
	out, sendErr := s.SSM.SendCommand(ctx, &ssm.SendCommandInput{InstanceIds: []string{instance.ID}, DocumentName: aws.String(sub.Binding.Document.Name), DocumentVersion: aws.String(sub.Binding.Document.Version), DocumentHash: aws.String(sub.Binding.Document.ContentSHA256), DocumentHashType: ssmtypes.DocumentHashTypeSha256, TimeoutSeconds: aws.Int32(int32(deliverySeconds)), Parameters: map[string][]string{"requestId": {sub.Binding.CommandID}, "payload": {encoded}, "stepTimeoutSeconds": {strconv.Itoa(payload.ExecTimeoutSeconds + 180)}}}, func(o *ssm.Options) { o.RetryMaxAttempts = 1; o.Retryer = aws.NopRetryer{} })
	if sendErr != nil {
		if requestRejected(sendErr) {
			sub.State = "submission_rejected"
			return sub, failure("submission_rejected", "SSM rejected execution; inspect credentials, target and pinned document permissions")
		}
		sub.State = "submission_unknown"
		return sub, failure("submission_unknown", "SSM submission could not be confirmed; retain the recovery ID and inspect its results; do not automatically execute again")
	}
	if out == nil || out.Command == nil || !execprotocol.ValidSSMCommandID(aws.ToString(out.Command.CommandId)) || (out.Command.DocumentName != nil && aws.ToString(out.Command.DocumentName) != sub.Binding.Document.Name) || (out.Command.DocumentVersion != nil && aws.ToString(out.Command.DocumentVersion) != sub.Binding.Document.Version) || (len(out.Command.InstanceIds) != 0 && (len(out.Command.InstanceIds) != 1 || out.Command.InstanceIds[0] != instance.ID)) {
		sub.State = "submission_unknown"
		return sub, failure("submission_unknown", "SSM returned an invalid execution acknowledgement; retain the recovery ID and inspect its results; do not automatically execute again")
	}
	sub.State, sub.SSMCommandID = "submitted", aws.ToString(out.Command.CommandId)
	// Do not put another network operation ahead of this recovery announcement.
	if progress != nil {
		if _, err := fmt.Fprintf(progress, "devbox: submitted command_id=%s ssm_command_id=%s\n", sub.Binding.CommandID, sub.SSMCommandID); err != nil {
			sub.DiagnosticCode = "recovery_output_failed"
			return sub, failure("recovery_output_failed", "execution was acknowledged but its recovery announcement could not be written; retain the returned command IDs")
		}
	}
	ack := execprotocol.Record{SchemaVersion: 1, Kind: "acknowledgement", Binding: sub.Binding, SSMCommandID: sub.SSMCommandID}
	ackBytes, _ := execprotocol.EncodeRecord(ack)
	key = execprotocol.ObjectKey(sub.Binding.Scope, sub.Binding.CommandID, "acknowledgement.json")
	if err := execprotocol.PutImmutable(ctx, s.Store, key, bytes.NewReader(ackBytes), int64(len(ackBytes)), execprotocol.Digest(ackBytes)); err != nil {
		sub.DiagnosticCode = "acknowledgement_publication_failed"
		return sub, nil
	}
	sub.AcknowledgementStored = true
	return sub, nil
}
