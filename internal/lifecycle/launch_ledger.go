package lifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Each request permits at most 256 immutable attempts and each record at most
// 1 MiB. Reaching either bound requires inspection; it never authorizes a fresh
// request or erases already discovered identities. Context deadlines also bound
// the exact-key reads. No bucket-wide scan is used to reconstruct lineage.
const MaxLaunchAttempts = 256
const MaxLaunchRecordBytes = 1 << 20

var (
	ErrLaunchRecordNotFound    = failure("launch_record_missing", "shared launch evidence is missing; inspect known workers without allocating again")
	ErrLaunchRecordConflict    = failure("launch_record_conflict", "immutable shared launch evidence differs; resume the existing request without allocating again")
	ErrLaunchLedgerCorrupt     = failure("launch_ledger_corrupt", "shared launch evidence has invalid scope, pins or lineage; inspect known workers without allocating again")
	ErrLaunchLedgerUnavailable = failure("launch_ledger_unavailable", "shared launch evidence is unavailable; restore access and resume without allocating again")
	launchAccountRE            = regexp.MustCompile(`^[0-9]{12}$`)
	launchDigestRE             = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type LaunchSnapshot struct {
	Receipt   BatchReceipt
	Prepared  map[string]PreparedAttempt
	Claims    map[string]DispatchClaim
	Responses map[string]AttemptResponse
}

type RecoveryLedger interface {
	BatchLedger
	Load(context.Context, string) (LaunchSnapshot, error)
}

type S3LaunchAPI interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// The descriptor and scope are private copies. A receipt cannot redirect reads
// or writes into a different account, deployment, bucket or ledger prefix.
type S3LaunchLedger struct {
	api        S3LaunchAPI
	scope      config.Config
	descriptor config.LaunchLedger
}

func NewS3LaunchLedger(api S3LaunchAPI, scope config.Config, descriptor config.LaunchLedger) (*S3LaunchLedger, error) {
	if api == nil || !launchAccountRE.MatchString(scope.ExpectedAccount) || scope.Region != "us-east-2" || !nameRE.MatchString(scope.Deployment) || !nameRE.MatchString(scope.Owner) || len(scope.Deployment) > 23 || len(scope.Owner) > 23 || descriptor.SchemaVersion != 1 || descriptor.ExpectedBucketOwner != scope.ExpectedAccount || descriptor.Region != scope.Region || descriptor.Prefix != config.LaunchLedgerPrefix(scope) {
		return nil, ErrLaunchLedgerCorrupt
	}
	// Reuse the established bucket and policy grammar, without applying result
	// expiration to launch records or treating the result prefix as authority.
	r := config.Results{SchemaVersion: 1, Bucket: descriptor.Bucket, ExpectedBucketOwner: descriptor.ExpectedBucketOwner, Region: descriptor.Region, Prefix: config.ResultsPrefix(scope), RetentionDays: 2, PolicySHA256: descriptor.PolicySHA256}
	if config.ValidateResults(r, scope) != nil {
		return nil, ErrLaunchLedgerCorrupt
	}
	return &S3LaunchLedger{api: api, scope: scope, descriptor: descriptor}, nil
}

func (l *S3LaunchLedger) key(request, attempt, record string) string {
	if attempt == "" {
		return l.descriptor.Prefix + request + "/plan.json"
	}
	return l.descriptor.Prefix + request + "/attempts/" + attempt + "/" + record + ".json"
}

func (l *S3LaunchLedger) validPlan(p LaunchPlan) bool {
	// Policy updates must not prevent observation of retained launch evidence.
	// New preparation/dispatch separately requires the exact current digest.
	location := p.LaunchLedger
	if !launchDigestRE.MatchString(location.PolicySHA256) {
		return false
	}
	location.PolicySHA256 = l.descriptor.PolicySHA256
	if p.Account != l.scope.ExpectedAccount || p.Region != l.scope.Region || p.Deployment != l.scope.Deployment || p.Owner != l.scope.Owner || location != l.descriptor || !launchDigestRE.MatchString(p.BootstrapSHA256) || !regexp.MustCompile(`^sg-([0-9a-f]{8}|[0-9a-f]{17})$`).MatchString(p.SecurityGroupID) || !regexp.MustCompile(`^arn:aws:iam::`+l.scope.ExpectedAccount+`:instance-profile/[A-Za-z0-9+=,.@_/-]+$`).MatchString(p.InstanceProfileARN) {
		return false
	}
	i := p.Image
	if i.Architecture != "x86_64" || i.UbuntuRelease != "24.04" || i.OwnerAccount != "099720109477" || !regexp.MustCompile(`^ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-[0-9.]+$`).MatchString(i.Name) || i.MinimumRootDiskGB < 1 || i.MinimumRootDiskGB > 16384 || i.RootDisk == nil || i.RootDisk.SizeGB < 8 || i.RootDisk.SizeGB < i.MinimumRootDiskGB || i.RootDisk.SizeGB > 16384 || i.RootDisk.Type != "gp3" || !i.RootDisk.Encrypted || !i.RootDisk.DeleteOnTermination {
		return false
	}
	v, err := strconv.ParseUint(p.Readiness.Version, 10, 64)
	if err != nil || v == 0 || strconv.FormatUint(v, 10) != p.Readiness.Version || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(p.Readiness.Name) || !launchDigestRE.MatchString(p.Readiness.ContentSHA256) || config.ValidateExecution(p.Execution) != nil || p.Execution.Name == p.Readiness.Name {
		return false
	}
	id, err := AttemptID(p.RequestID, "")
	if err != nil {
		return false
	}
	a := AttemptReceipt{AttemptID: id, ClientToken: attemptToken(p.Digest(), id, p.RequestedCount), RequestedCount: p.RequestedCount, CreatedAt: p.CreatedAt, State: "prepared", InstanceIDs: []string{}, Errors: []ResourceError{}}
	_, err = BuildFleetInput(p, a)
	return err == nil
}

func validPrepared(r BatchReceipt, p PreparedAttempt) bool {
	if p.SchemaVersion != 1 || p.RequestID != r.RequestID || p.PlanSHA256 != r.PlanSHA256 || p.Attempt.State != "prepared" || p.Attempt.FleetID != "" || p.Attempt.InstanceIDs == nil || len(p.Attempt.InstanceIDs) != 0 || p.Attempt.Errors == nil || len(p.Attempt.Errors) != 0 {
		return false
	}
	if p.Attempt.ParentID == "" && p.Attempt.CreatedAt != r.Plan.CreatedAt {
		return false
	}
	input, err := BuildFleetInput(r.Plan, p.Attempt)
	if err != nil || p.InputSHA256 != fleetInputDigest(input) {
		return false
	}
	candidate := cloneBatch(r)
	candidate.Attempts = append(candidate.Attempts, p.Attempt)
	return candidate.Validate() == nil
}

func expectedClaim(p PreparedAttempt) DispatchClaim {
	return DispatchClaim{SchemaVersion: 1, RequestID: p.RequestID, AttemptID: p.Attempt.AttemptID, PlanSHA256: p.PlanSHA256, InputSHA256: p.InputSHA256, ClientToken: p.Attempt.ClientToken}
}

func validResponse(r BatchReceipt, p PreparedAttempt, response AttemptResponse) bool {
	if response.SchemaVersion != 1 || response.RequestID != p.RequestID || response.PlanSHA256 != p.PlanSHA256 || response.InputSHA256 != p.InputSHA256 || response.Workers == nil || response.Attempt.InstanceIDs == nil || response.Attempt.Errors == nil {
		return false
	}
	a := response.Attempt
	if a.State != "unknown" && a.State != "complete" && a.State != "rejected" {
		return false
	}
	if a.FleetID != "" && !fleetIDRE.MatchString(a.FleetID) {
		return false
	}
	base := a
	base.State, base.FleetID, base.InstanceIDs, base.Errors = "prepared", "", []string{}, []ResourceError{}
	if !reflect.DeepEqual(base, p.Attempt) {
		return false
	}
	candidate := cloneBatch(r)
	candidate.Attempts = append(candidate.Attempts, a)
	if candidate.Validate() != nil || len(response.Workers) != len(a.InstanceIDs) {
		return false
	}
	seen := map[string]bool{}
	for _, w := range response.Workers {
		if seen[w.ID] {
			return false
		}
		seen[w.ID] = true
		choice := config.LaunchChoice{InstanceType: w.Type, SubnetID: w.SubnetID, AvailabilityZone: w.AvailabilityZone}
		pool := choice == (config.LaunchChoice{}) && a.State == "unknown"
		for _, allowed := range r.Plan.Choices {
			if choice == allowed {
				pool = true
			}
		}
		if !pool || !reflect.DeepEqual(w, fleetWorker(r.Plan, p.Attempt, w.ID, choice)) {
			return false
		}
	}
	for _, id := range a.InstanceIDs {
		if !seen[id] {
			return false
		}
	}
	if a.State == "complete" && len(a.InstanceIDs) < a.RequestedCount && len(a.Errors) == 0 {
		return false
	}
	if a.State == "rejected" && len(a.Errors) == 0 {
		return false
	}
	for _, diagnostic := range a.Errors {
		if !validLaunchDiagnostic(r.Plan, a.State, diagnostic) {
			return false
		}
	}
	return true
}

// Shared responses preserve the allocator's sanitized diagnostic vocabulary.
// Corrupt records cannot introduce arbitrary service text into later output.
func validLaunchDiagnostic(plan LaunchPlan, state string, diagnostic ResourceError) bool {
	known := false
	for _, code := range []string{"UnauthorizedOperation", "InsufficientInstanceCapacity", "MaxSpotInstanceCountExceeded", "InvalidParameter", "PendingVerification", "RequestLimitExceeded", "IdempotentParameterMismatch", "unknown"} {
		canonical, rejected := fleetDiagnostic(code)
		if diagnostic.Code == canonical.Code && diagnostic.Message == canonical.Message && (state != "rejected" || rejected) {
			known = true
		}
	}
	switch diagnostic.Code {
	case "fleet_pool_error":
		known = state != "rejected" && diagnostic.Message == "EC2 reported an unrecognized pool error; retain successful workers and inspect the verified allocation outcome before retrying."
	case "fleet_response_incomplete":
		known = state == "unknown" && diagnostic.Message == "The Fleet response does not bound allocation; preserve all known identities and resume without allocating."
	case "fleet_response_conflict":
		return state == "unknown" && fleetIDRE.MatchString(diagnostic.ResourceID) && diagnostic.InstanceType == "" && diagnostic.SubnetID == "" && diagnostic.Message == "The response reports a different Fleet identity; preserve both identities and resume without allocating."
	}
	if !known || diagnostic.ResourceID != "" {
		return false
	}
	if diagnostic.InstanceType == "" && diagnostic.SubnetID == "" {
		return true
	}
	for _, choice := range plan.Choices {
		if diagnostic.InstanceType == choice.InstanceType && diagnostic.SubnetID == choice.SubnetID {
			return true
		}
	}
	return false
}

// Prepare checks shared historical responses before publishing a successor.
// Local observed counts and states never establish the remaining capacity.
// Concurrent preparations with different timestamps conflict; callers Load the
// winning prepared record and observe it, rather than gaining dispatch consent.
func (l *S3LaunchLedger) Prepare(ctx context.Context, receipt BatchReceipt, p PreparedAttempt) error {
	if !l.validPlan(receipt.Plan) || receipt.Plan.LaunchLedger != l.descriptor || receipt.Validate() != nil || len(receipt.Attempts) > MaxLaunchAttempts || !reflect.DeepEqual(receipt.Attempts[len(receipt.Attempts)-1], p.Attempt) {
		return ErrLaunchLedgerCorrupt
	}
	prior := cloneBatch(receipt)
	prior.Attempts = prior.Attempts[:len(prior.Attempts)-1]
	if !validPrepared(prior, p) {
		return ErrLaunchLedgerCorrupt
	}
	if err := l.putImmutable(ctx, l.key(receipt.RequestID, "", ""), receipt.Plan); err != nil {
		return err
	}
	snapshot, err := l.Load(ctx, receipt.RequestID)
	if err != nil {
		return err
	}
	if snapshot.Receipt.PlanSHA256 != receipt.PlanSHA256 {
		return ErrLaunchRecordConflict
	}
	if existing, ok := snapshot.Prepared[p.Attempt.AttemptID]; ok {
		if !reflect.DeepEqual(existing, p) {
			return ErrLaunchRecordConflict
		}
		// Require the same entire prefix, even when this attempt already exists.
		if len(snapshot.Receipt.Attempts) != len(receipt.Attempts) {
			return ErrLaunchRecordConflict
		}
		snapshot.Receipt.Attempts = snapshot.Receipt.Attempts[:len(snapshot.Receipt.Attempts)-1]
	}
	if !reflect.DeepEqual(snapshot.Receipt.Attempts, prior.Attempts) {
		return ErrLaunchRecordConflict
	}
	return l.putImmutable(ctx, l.key(receipt.RequestID, p.Attempt.AttemptID, "prepared"), p)
}

func (l *S3LaunchLedger) Claim(ctx context.Context, descriptor config.LaunchLedger, claim DispatchClaim) (bool, error) {
	if descriptor != l.descriptor || !ValidRequest(claim.RequestID) || !ValidRequest(claim.AttemptID) {
		return false, ErrLaunchLedgerCorrupt
	}
	snapshot, err := l.Load(ctx, claim.RequestID)
	if err != nil {
		return false, err
	}
	if snapshot.Receipt.Plan.LaunchLedger != l.descriptor {
		return false, ErrLaunchRecordConflict
	}
	p, ok := snapshot.Prepared[claim.AttemptID]
	if !ok || claim != expectedClaim(p) {
		return false, ErrLaunchLedgerCorrupt
	}
	if _, exists := snapshot.Claims[claim.AttemptID]; exists {
		return false, nil
	}
	if len(snapshot.Receipt.Attempts) == 0 || snapshot.Receipt.Attempts[len(snapshot.Receipt.Attempts)-1].AttemptID != claim.AttemptID {
		return false, ErrLaunchRecordConflict
	}
	// NEVER reconcile this PUT by GET or authorize from a subsequent retry.
	// Even a persisted object with a lost acknowledgment permanently loses the
	// calling process's authority to dispatch this attempt.
	err = l.put(ctx, l.key(claim.RequestID, claim.AttemptID, "dispatch"), claim)
	if errors.Is(err, ErrLaunchRecordConflict) {
		return false, nil
	}
	return err == nil, err
}

// RecordResponse may preserve evidence for an existing original claim after a
// policy-digest update at the same storage location. It never grants dispatch,
// changes the original pins, or relaxes Prepare/Claim's current-policy checks.
func (l *S3LaunchLedger) RecordResponse(ctx context.Context, descriptor config.LaunchLedger, response AttemptResponse) error {
	if descriptor != l.descriptor || !ValidRequest(response.RequestID) || !ValidRequest(response.Attempt.AttemptID) {
		return ErrLaunchLedgerCorrupt
	}
	snapshot, err := l.Load(ctx, response.RequestID)
	if err != nil {
		return err
	}
	p, ok := snapshot.Prepared[response.Attempt.AttemptID]
	if !ok || snapshot.Claims[p.Attempt.AttemptID] != expectedClaim(p) {
		return ErrLaunchLedgerCorrupt
	}
	prior := cloneBatch(snapshot.Receipt)
	for n, a := range prior.Attempts {
		if a.AttemptID == p.Attempt.AttemptID {
			prior.Attempts = prior.Attempts[:n]
			break
		}
	}
	if !validResponse(prior, p, response) {
		return ErrLaunchLedgerCorrupt
	}
	return l.putImmutable(ctx, l.key(response.RequestID, p.Attempt.AttemptID, "response"), response)
}

// Load reconstructs only immutable shared evidence. It returns all valid earlier
// records with any later error. A plan-only request has zero attempts; a claim
// without a response remains dispatched, and cannot imply zero allocation.
func (l *S3LaunchLedger) Load(ctx context.Context, request string) (LaunchSnapshot, error) {
	s := LaunchSnapshot{Prepared: map[string]PreparedAttempt{}, Claims: map[string]DispatchClaim{}, Responses: map[string]AttemptResponse{}}
	if !ValidRequest(request) {
		return s, ErrLaunchLedgerCorrupt
	}
	var plan LaunchPlan
	if err := l.read(ctx, l.key(request, "", ""), &plan); err != nil {
		return s, err
	}
	if plan.RequestID != request || !l.validPlan(plan) {
		return s, ErrLaunchLedgerCorrupt
	}
	s.Receipt = BatchReceipt{SchemaVersion: batchVersion(plan.SchemaVersion), RequestID: request, Plan: plan, PlanSHA256: plan.Digest(), Attempts: []AttemptReceipt{}}
	parent := ""
	for n := 0; n <= MaxLaunchAttempts; n++ {
		id, _ := AttemptID(request, parent)
		var prepared PreparedAttempt
		err := l.read(ctx, l.key(request, id, "prepared"), &prepared)
		if errors.Is(err, ErrLaunchRecordNotFound) {
			// A missing preparation cannot hide an orphaned claim/response.
			for _, record := range []string{"dispatch", "response"} {
				_, absent := l.get(ctx, l.key(request, id, record))
				if !errors.Is(absent, ErrLaunchRecordNotFound) {
					if absent != nil {
						return s, absent
					}
					return s, ErrLaunchLedgerCorrupt
				}
			}
			return s, nil
		}
		if err != nil {
			return s, err
		}
		if n == MaxLaunchAttempts || prepared.Attempt.AttemptID != id || !validPrepared(s.Receipt, prepared) {
			return s, ErrLaunchLedgerCorrupt
		}
		prior := cloneBatch(s.Receipt)
		s.Prepared[id] = prepared
		s.Receipt.Attempts = append(s.Receipt.Attempts, prepared.Attempt)
		var claim DispatchClaim
		claimErr := l.read(ctx, l.key(request, id, "dispatch"), &claim)
		if claimErr == nil && claim != expectedClaim(prepared) {
			claimErr = ErrLaunchLedgerCorrupt
		}
		if claimErr == nil {
			s.Claims[id] = claim
			s.Receipt.Attempts[n].State = "dispatched"
		}
		var response AttemptResponse
		responseErr := l.read(ctx, l.key(request, id, "response"), &response)
		if responseErr == nil {
			if !validResponse(prior, prepared, response) {
				return s, ErrLaunchLedgerCorrupt
			}
			s.Responses[id] = response
			s.Receipt.Attempts[n] = response.Attempt
			if claimErr != nil {
				s.Receipt.Attempts[n].State = "unknown"
				if errors.Is(claimErr, ErrLaunchRecordNotFound) {
					return s, ErrLaunchLedgerCorrupt
				}
				return s, claimErr
			}
		} else if !errors.Is(responseErr, ErrLaunchRecordNotFound) {
			return s, responseErr
		}
		if claimErr != nil && !errors.Is(claimErr, ErrLaunchRecordNotFound) {
			return s, claimErr
		}
		parent = id
	}
	return s, ErrLaunchLedgerCorrupt
}

func launchCredentials(o *s3.Options) {
	o.Credentials = execprotocol.SanitizedCredentials(o.Credentials)
}

func launchStorageError(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrLaunchRecordNotFound, ErrLaunchRecordConflict, ErrLaunchLedgerCorrupt, ErrLaunchLedgerUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	for _, credential := range []struct {
		err           error
		code, message string
	}{
		{execprotocol.ErrCredentialsExpired, "credentials_expired", "launch storage credentials expired; refresh the selected profile and resume"},
		{execprotocol.ErrCredentialsInvalid, "credentials_invalid", "launch storage credentials are invalid; repair the selected profile and resume"},
		{execprotocol.ErrCredentialsUnavailable, "credentials_unavailable", "launch storage credentials are unavailable; repair the selected profile and resume"},
	} {
		if errors.Is(err, credential.err) {
			return failure(credential.code, credential.message)
		}
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return ErrLaunchRecordNotFound
		case "PreconditionFailed", "ConditionalRequestConflict":
			return ErrLaunchRecordConflict
		case "AccessDenied", "AccessDeniedException":
			return failure("launch_ledger_denied", "shared launch storage denied access; restore access and resume without allocating again")
		case "ExpiredToken", "ExpiredTokenException", "RequestExpired", "TokenRefreshRequired":
			return failure("credentials_expired", "launch storage credentials expired; refresh the selected profile and resume")
		case "InvalidClientTokenId", "UnrecognizedClientException", "SignatureDoesNotMatch", "InvalidAccessKeyId", "InvalidToken", "AuthFailure", "RequestTimeTooSkewed":
			return failure("credentials_invalid", "launch storage credentials are invalid; repair the selected profile and resume")
		}
	}
	return ErrLaunchLedgerUnavailable
}

func (l *S3LaunchLedger) getOnce(ctx context.Context, key string) ([]byte, error) {
	out, err := l.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(l.descriptor.Bucket), Key: aws.String(key), ExpectedBucketOwner: aws.String(l.descriptor.ExpectedBucketOwner)}, launchCredentials)
	if err != nil {
		return nil, launchStorageError(err)
	}
	if out == nil || out.Body == nil {
		return nil, ErrLaunchLedgerCorrupt
	}
	defer out.Body.Close()
	if out.ContentLength == nil || *out.ContentLength < 1 || *out.ContentLength > MaxLaunchRecordBytes {
		return nil, ErrLaunchLedgerCorrupt
	}
	b, err := io.ReadAll(io.LimitReader(out.Body, MaxLaunchRecordBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrLaunchLedgerUnavailable
	}
	if int64(len(b)) != *out.ContentLength {
		return nil, ErrLaunchLedgerCorrupt
	}
	return b, nil
}

func (l *S3LaunchLedger) get(ctx context.Context, key string) ([]byte, error) {
	b, err := l.getOnce(ctx, key)
	var denied *Failure
	if !errors.As(err, &denied) || denied.Code != "launch_ledger_denied" {
		return b, err
	}
	// Prefix-scoped IAM can make missing GetObject look denied. An exact,
	// authenticated one-key listing can prove absence, never dispatch authority.
	out, listErr := l.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(l.descriptor.Bucket), ExpectedBucketOwner: aws.String(l.descriptor.ExpectedBucketOwner), Prefix: aws.String(key), MaxKeys: aws.Int32(1)}, launchCredentials)
	if listErr != nil {
		return nil, launchStorageError(listErr)
	}
	if out != nil && aws.ToString(out.Name) == l.descriptor.Bucket && aws.ToString(out.Prefix) == key && len(out.Contents) <= 1 {
		if len(out.Contents) == 0 && !aws.ToBool(out.IsTruncated) {
			return nil, ErrLaunchRecordNotFound
		}
		if len(out.Contents) == 1 {
			found := aws.ToString(out.Contents[0].Key)
			if found == key {
				return l.getOnce(ctx, key)
			}
			if strings.HasPrefix(found, key) {
				return nil, ErrLaunchRecordNotFound
			}
		}
	}
	return nil, err
}

func (l *S3LaunchLedger) read(ctx context.Context, key string, dst any) error {
	b, err := l.get(ctx, key)
	if err != nil {
		return err
	}
	if !uniqueJSON(b) {
		return ErrLaunchLedgerCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return ErrLaunchLedgerCorrupt
	}
	return nil
}

// encoding/json accepts repeated object keys. Reject those before decoding
// immutable evidence so two interpretations cannot share an apparent record.
func uniqueJSON(b []byte) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, nested := token.(json.Delim)
		if !nested {
			return true
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || keys[name] {
					return false
				}
				keys[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func (l *S3LaunchLedger) put(ctx context.Context, key string, record any) error {
	b, err := json.Marshal(record)
	if err != nil || len(b) == 0 || len(b) > MaxLaunchRecordBytes {
		return ErrLaunchLedgerCorrupt
	}
	sum := sha256.Sum256(b)
	out, err := l.api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(l.descriptor.Bucket), Key: aws.String(key), ExpectedBucketOwner: aws.String(l.descriptor.ExpectedBucketOwner), IfNoneMatch: aws.String("*"), ServerSideEncryption: s3types.ServerSideEncryptionAes256, ContentLength: aws.Int64(int64(len(b))), Body: bytes.NewReader(b), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:]))}, launchCredentials, func(o *s3.Options) { o.RetryMaxAttempts = 1; o.Retryer = aws.NopRetryer{} })
	if err != nil {
		return launchStorageError(err)
	}
	if out == nil {
		return ErrLaunchLedgerUnavailable
	}
	return nil
}

func (l *S3LaunchLedger) putImmutable(ctx context.Context, key string, record any) error {
	err := l.put(ctx, key, record)
	if err == nil {
		return nil
	}
	// Metadata may reconcile a lost write acknowledgement, claims never may.
	existing, readErr := l.get(ctx, key)
	if readErr != nil {
		if errors.Is(readErr, ErrLaunchRecordNotFound) {
			return err
		}
		return readErr
	}
	want, _ := json.Marshal(record)
	if !bytes.Equal(existing, want) {
		return ErrLaunchRecordConflict
	}
	return nil
}
