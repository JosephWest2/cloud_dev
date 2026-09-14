package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type launchMemoryS3 struct {
	mu            sync.Mutex
	objects       map[string][]byte
	getErrors     map[string]error
	deniedMissing bool
	listResult    *s3.ListObjectsV2Output
	lists         []*s3.ListObjectsV2Input
	claimBarrier  *sync.WaitGroup
	claimPuts     int
}

func (s *launchMemoryS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := aws.ToString(in.Key)
	if err := s.getErrors[key]; err != nil {
		return nil, err
	}
	b, ok := s.objects[key]
	if !ok {
		code := "NoSuchKey"
		if s.deniedMissing {
			code = "AccessDenied"
		}
		return nil, &smithy.GenericAPIError{Code: code, Message: "PRIVATE service details"}
	}
	b = append([]byte(nil), b...)
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b)), ContentLength: aws.Int64(int64(len(b)))}, nil
}

func (s *launchMemoryS3) PutObject(_ context.Context, in *s3.PutObjectInput, options ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(in.Key)
	claim := strings.HasSuffix(key, "/dispatch.json")
	if claim && s.claimBarrier != nil {
		s.claimBarrier.Done()
		s.claimBarrier.Wait()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if claim {
		s.claimPuts++
	}
	if aws.ToString(in.IfNoneMatch) != "*" || in.ServerSideEncryption != s3types.ServerSideEncryptionAes256 || aws.ToString(in.ExpectedBucketOwner) == "" || in.ChecksumSHA256 == nil {
		return nil, errors.New("unsafe write")
	}
	o := s3.Options{}
	for _, option := range options {
		option(&o)
	}
	if o.RetryMaxAttempts != 1 || o.Retryer.MaxAttempts() != 1 {
		return nil, errors.New("retrying write")
	}
	if _, exists := s.objects[key]; exists {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed"}
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	s.objects[key] = b
	return &s3.PutObjectOutput{}, nil
}

func (s *launchMemoryS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists = append(s.lists, in)
	if s.listResult != nil {
		return s.listResult, nil
	}
	return &s3.ListObjectsV2Output{Name: in.Bucket, Prefix: in.Prefix, IsTruncated: aws.Bool(false)}, nil
}

func ledgerFixture(t *testing.T) (*S3LaunchLedger, *launchMemoryS3, config.Config, BatchReceipt, PreparedAttempt) {
	t.Helper()
	c, m, p, selection := batchFixture(t)
	r, err := NewBatchReceipt(c, m, p, selection)
	if err != nil {
		t.Fatal(err)
	}
	prepared := ledgerPrepared(t, r)
	api := &launchMemoryS3{objects: map[string][]byte{}, getErrors: map[string]error{}}
	l, err := NewS3LaunchLedger(api, c, r.Plan.LaunchLedger)
	if err != nil {
		t.Fatal(err)
	}
	return l, api, c, r, prepared
}

func ledgerPrepared(t *testing.T, r BatchReceipt) PreparedAttempt {
	t.Helper()
	a := r.Attempts[len(r.Attempts)-1]
	in, err := BuildFleetInput(r.Plan, a)
	if err != nil {
		t.Fatal(err)
	}
	return PreparedAttempt{SchemaVersion: 1, RequestID: r.RequestID, PlanSHA256: r.PlanSHA256, InputSHA256: fleetInputDigest(in), Attempt: a}
}

func ledgerResponse(r BatchReceipt, p PreparedAttempt, ids ...string) AttemptResponse {
	a := p.Attempt
	a.State, a.FleetID = "complete", fleetWireID
	a.InstanceIDs = append([]string{}, ids...)
	if len(ids) < a.RequestedCount {
		diagnostic, _ := fleetDiagnostic("InsufficientInstanceCapacity")
		diagnostic.InstanceType = r.Plan.Choices[0].InstanceType
		diagnostic.SubnetID = r.Plan.Choices[0].SubnetID
		a.Errors = []ResourceError{diagnostic}
	}
	response := AttemptResponse{SchemaVersion: 1, RequestID: r.RequestID, PlanSHA256: r.PlanSHA256, InputSHA256: p.InputSHA256, Attempt: a, Workers: []WorkerOutcome{}}
	for _, id := range ids {
		response.Workers = append(response.Workers, fleetWorker(r.Plan, p.Attempt, id, r.Plan.Choices[0]))
	}
	return response
}

func ledgerSuccessor(t *testing.T, r BatchReceipt) (BatchReceipt, PreparedAttempt) {
	t.Helper()
	last := r.Attempts[len(r.Attempts)-1]
	id, _ := AttemptID(r.RequestID, last.AttemptID)
	count := r.Plan.RequestedCount - len(r.FulfilledIDs())
	r = cloneBatch(r)
	r.Attempts = append(r.Attempts, AttemptReceipt{AttemptID: id, ParentID: last.AttemptID, ClientToken: attemptToken(r.PlanSHA256, id, count), RequestedCount: count, CreatedAt: r.Plan.CreatedAt, State: "prepared", InstanceIDs: []string{}, Errors: []ResourceError{}})
	return r, ledgerPrepared(t, r)
}

func ledgerPublishResponse(t *testing.T, l *S3LaunchLedger, r BatchReceipt, p PreparedAttempt, ids ...string) BatchReceipt {
	t.Helper()
	ctx := context.Background()
	if err := l.Prepare(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	if won, err := l.Claim(ctx, r.Plan.LaunchLedger, expectedClaim(p)); err != nil || !won {
		t.Fatalf("claim: %v %v", won, err)
	}
	response := ledgerResponse(r, p, ids...)
	if err := l.RecordResponse(ctx, r.Plan.LaunchLedger, response); err != nil {
		t.Fatal(err)
	}
	r = cloneBatch(r)
	r.Attempts[len(r.Attempts)-1] = response.Attempt
	return r
}

func TestLaunchLedgerSeparateClientsCompeteForOneConditionalClaim(t *testing.T) {
	l, api, c, r, p := ledgerFixture(t)
	if err := l.Prepare(context.Background(), r, p); err != nil {
		t.Fatal(err)
	}
	other, _ := NewS3LaunchLedger(api, c, r.Plan.LaunchLedger)
	api.claimBarrier = &sync.WaitGroup{}
	api.claimBarrier.Add(2)
	winners := make(chan bool, 2)
	for _, client := range []*S3LaunchLedger{l, other} {
		go func(client *S3LaunchLedger) {
			won, err := client.Claim(context.Background(), r.Plan.LaunchLedger, expectedClaim(p))
			if err != nil {
				t.Error(err)
			}
			winners <- won
		}(client)
	}
	a, b := <-winners, <-winners
	if a == b || api.claimPuts != 2 {
		t.Fatalf("conditional exclusion: winners=%v,%v puts=%d", a, b, api.claimPuts)
	}
	api.claimBarrier = nil
	if won, err := other.Claim(context.Background(), r.Plan.LaunchLedger, expectedClaim(p)); err != nil || won {
		t.Fatalf("existing record authorized send: %v %v", won, err)
	}
}

func TestLaunchLedgerSharedHistoryBoundsSuccessorsAndRetainsIdentities(t *testing.T) {
	l, api, _, r, p := ledgerFixture(t)
	ctx := context.Background()
	r = ledgerPublishResponse(t, l, r, p, "i-0123456789abcdef0")
	next, prepared := ledgerSuccessor(t, r)
	if prepared.Attempt.RequestedCount != 1 {
		t.Fatal("original fulfillment not deducted")
	}
	if err := l.Prepare(ctx, next, prepared); err != nil {
		t.Fatal(err)
	}
	snapshot, err := l.Load(ctx, r.RequestID)
	if err != nil || !reflect.DeepEqual(snapshot.Receipt.FulfilledIDs(), []string{"i-0123456789abcdef0"}) || snapshot.Receipt.Attempts[1].State != "prepared" {
		t.Fatalf("lost history: %+v %v", snapshot.Receipt, err)
	}
	// A forged local complete response cannot erase historical success, even
	// if local lineage/count/token validation is internally consistent.
	forged := cloneBatch(r)
	forged.Attempts[0].InstanceIDs = []string{}
	forged, bad := ledgerSuccessor(t, forged)
	if err := l.Prepare(ctx, forged, bad); err == nil {
		t.Fatal("cached zero rewrote original fulfillment")
	}
	// A valid earlier ID must survive corrupted later immutable preparation.
	api.objects[l.key(r.RequestID, prepared.Attempt.AttemptID, "prepared")] = []byte(`{"schema_version": 99}`)
	snapshot, err = l.Load(ctx, r.RequestID)
	if !errors.Is(err, ErrLaunchLedgerCorrupt) || !reflect.DeepEqual(snapshot.Receipt.FulfilledIDs(), []string{"i-0123456789abcdef0"}) {
		t.Fatalf("later corruption erased history: %+v %v", snapshot.Receipt, err)
	}
}

func TestLaunchLedgerPreparedAndMissingResponseNeverProveZero(t *testing.T) {
	l, _, _, r, p := ledgerFixture(t)
	ctx := context.Background()
	if err := l.Prepare(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	snapshot, err := l.Load(ctx, r.RequestID)
	if err != nil || snapshot.Receipt.Attempts[0].State != "prepared" {
		t.Fatal("pre-dispatch preparation lost")
	}
	if _, err := snapshot.Receipt.MissingCapacity(p.Attempt.AttemptID); err == nil {
		t.Fatal("prepared inferred zero")
	}
	if won, err := l.Claim(ctx, r.Plan.LaunchLedger, expectedClaim(p)); err != nil || !won {
		t.Fatal("claim failed")
	}
	snapshot, err = l.Load(ctx, r.RequestID)
	if err != nil || snapshot.Receipt.Attempts[0].State != "dispatched" {
		t.Fatal("missing response became terminal")
	}
	if _, err := snapshot.Receipt.MissingCapacity(p.Attempt.AttemptID); err == nil {
		t.Fatal("missing response inferred zero")
	}
	forged := cloneBatch(snapshot.Receipt)
	forged.Attempts[0].State, forged.Attempts[0].FleetID = "complete", fleetWireID
	forged.Attempts[0].Errors = []ResourceError{{Code: "capacity_unavailable"}}
	next, prepared := ledgerSuccessor(t, forged)
	if err := l.Prepare(ctx, next, prepared); err == nil {
		t.Fatal("local response authorized successor")
	}
}

func TestLaunchLedgerPlanOnlyAndCanonicalPreparationConflict(t *testing.T) {
	l, api, _, r, p := ledgerFixture(t)
	api.objects[l.key(r.RequestID, "", "")], _ = json.Marshal(r.Plan)
	snapshot, err := l.Load(context.Background(), r.RequestID)
	if err != nil || snapshot.Receipt.PlanSHA256 != r.PlanSHA256 || len(snapshot.Receipt.Attempts) != 0 {
		t.Fatalf("plan-only recovery: %+v %v", snapshot, err)
	}
	r = ledgerPublishResponse(t, l, r, p, "i-0123456789abcdef0")
	next, prepared := ledgerSuccessor(t, r)
	if err := l.Prepare(context.Background(), next, prepared); err != nil {
		t.Fatal(err)
	}
	other := cloneBatch(next)
	other.Attempts[1].CreatedAt = "2026-09-15T00:00:00Z"
	if err := l.Prepare(context.Background(), other, ledgerPrepared(t, other)); !errors.Is(err, ErrLaunchRecordConflict) {
		t.Fatalf("different preparation did not conflict: %v", err)
	}
	snapshot, err = l.Load(context.Background(), r.RequestID)
	if err != nil || !reflect.DeepEqual(snapshot.Prepared[prepared.Attempt.AttemptID], prepared) {
		t.Fatal("winning preparation was overwritten")
	}
}

func TestLaunchLedgerRejectsCorruptRecordsAndOrphanedResponses(t *testing.T) {
	for _, name := range []string{"scope", "unknown-json", "duplicate-json", "oversized", "prepared-hash", "prepared-token", "dispatch-hash", "response-token", "response-workers", "orphan-response", "orphan-claim"} {
		t.Run(name, func(t *testing.T) {
			l, api, _, r, p := ledgerFixture(t)
			ctx := context.Background()
			r = ledgerPublishResponse(t, l, r, p, "i-0123456789abcdef0")
			key := l.key(r.RequestID, "", "")
			switch name {
			case "scope":
				plan := r.Plan
				plan.Owner = "other"
				api.objects[key], _ = json.Marshal(plan)
			case "unknown-json":
				api.objects[key] = append([]byte(`{"unexpected":true,`), api.objects[key][1:]...)
			case "duplicate-json":
				api.objects[key] = append([]byte(`{"schema_version":1,`), api.objects[key][1:]...)
			case "oversized":
				api.objects[key] = []byte(strings.Repeat(" ", MaxLaunchRecordBytes+1))
			case "prepared-hash", "prepared-token":
				bad := p
				if name == "prepared-hash" {
					bad.InputSHA256 = strings.Repeat("a", 64)
				} else {
					bad.Attempt.ClientToken = strings.Repeat("b", 64)
				}
				api.objects[l.key(r.RequestID, p.Attempt.AttemptID, "prepared")], _ = json.Marshal(bad)
			case "dispatch-hash":
				bad := expectedClaim(p)
				bad.InputSHA256 = strings.Repeat("a", 64)
				api.objects[l.key(r.RequestID, p.Attempt.AttemptID, "dispatch")], _ = json.Marshal(bad)
			case "response-token", "response-workers":
				bad := ledgerResponse(r, p, "i-0123456789abcdef0")
				if name == "response-token" {
					bad.Attempt.ClientToken = strings.Repeat("a", 64)
				} else {
					bad.Workers[0].Image = "ami-00000000"
				}
				api.objects[l.key(r.RequestID, p.Attempt.AttemptID, "response")], _ = json.Marshal(bad)
			case "orphan-response":
				delete(api.objects, l.key(r.RequestID, p.Attempt.AttemptID, "dispatch"))
			case "orphan-claim":
				delete(api.objects, l.key(r.RequestID, p.Attempt.AttemptID, "prepared"))
			}
			snapshot, err := l.Load(ctx, r.RequestID)
			if !errors.Is(err, ErrLaunchLedgerCorrupt) {
				t.Fatalf("corruption accepted: %v", err)
			}
			if name == "dispatch-hash" || name == "orphan-response" {
				if !reflect.DeepEqual(snapshot.Receipt.FulfilledIDs(), []string{"i-0123456789abcdef0"}) || snapshot.Receipt.Attempts[0].State != "unknown" {
					t.Fatal("valid response identities lost with invalid claim")
				}
			}
		})
	}
}

func TestLaunchLedgerChangedPolicyAllowsObservationButNotAllocation(t *testing.T) {
	l, api, c, r, p := ledgerFixture(t)
	r = ledgerPublishResponse(t, l, r, p, "i-0123456789abcdef0")
	updated := r.Plan.LaunchLedger
	updated.PolicySHA256 = strings.Repeat("b", 64)
	current, err := NewS3LaunchLedger(api, c, updated)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := current.Load(context.Background(), r.RequestID)
	if err != nil || snapshot.Receipt.PlanSHA256 != r.PlanSHA256 || snapshot.Receipt.Plan.LaunchLedger.PolicySHA256 != r.Plan.LaunchLedger.PolicySHA256 {
		t.Fatalf("policy update rewrote history: %v", err)
	}
	next, prepared := ledgerSuccessor(t, r)
	if err := current.Prepare(context.Background(), next, prepared); err == nil {
		t.Fatal("changed policy permitted new dispatch preparation")
	}
}

func TestLaunchLedgerRetainsExistingClaimResponseAfterPolicyUpdate(t *testing.T) {
	l, api, c, r, prepared := ledgerFixture(t)
	ctx := context.Background()
	if err := l.Prepare(ctx, r, prepared); err != nil {
		t.Fatal(err)
	}
	if won, err := l.Claim(ctx, r.Plan.LaunchLedger, expectedClaim(prepared)); err != nil || !won {
		t.Fatal("original claim failed", err)
	}
	updated := r.Plan.LaunchLedger
	updated.PolicySHA256 = strings.Repeat("b", 64)
	current, err := NewS3LaunchLedger(api, c, updated)
	if err != nil {
		t.Fatal(err)
	}
	response := ledgerResponse(r, prepared, "i-0123456789abcdef0")
	if err = current.RecordResponse(ctx, updated, response); err != nil {
		t.Fatal("lost original dispatch evidence after policy update", err)
	}
	loaded, err := current.Load(ctx, r.RequestID)
	if err != nil || loaded.Receipt.PlanSHA256 != r.PlanSHA256 || len(loaded.Receipt.FulfilledIDs()) != 1 {
		t.Fatal("old claim evidence changed", err)
	}
	if won, err := current.Claim(ctx, updated, expectedClaim(prepared)); err == nil || won {
		t.Fatal("updated policy granted an old-plan dispatch")
	}
	next, nextPrepared := ledgerSuccessor(t, loaded.Receipt)
	if err := current.Prepare(ctx, next, nextPrepared); err == nil {
		t.Fatal("updated policy granted old-plan preparation")
	}
}

func TestLaunchLedgerDeniedMissingReadUsesOnlyBoundedExactPrefix(t *testing.T) {
	l, api, _, r, _ := ledgerFixture(t)
	api.deniedMissing = true
	_, err := l.Load(context.Background(), r.RequestID)
	if !errors.Is(err, ErrLaunchRecordNotFound) || len(api.lists) != 1 {
		t.Fatalf("absence fallback failed: %v", err)
	}
	in := api.lists[0]
	if aws.ToString(in.Prefix) != l.key(r.RequestID, "", "") || aws.ToInt32(in.MaxKeys) != 1 || aws.ToString(in.ExpectedBucketOwner) != r.Plan.Account {
		t.Fatal("unbounded or unscoped listing")
	}
	api.listResult = &s3.ListObjectsV2Output{Name: aws.String("other-bucket"), Prefix: in.Prefix}
	if _, err := l.Load(context.Background(), r.RequestID); errors.Is(err, ErrLaunchRecordNotFound) || err == nil {
		t.Fatal("malformed list proved absence")
	}
	api.listResult = nil
	api.getErrors[l.key(r.RequestID, "", "")] = &smithy.GenericAPIError{Code: "ExpiredToken", Message: "PRIVATE"}
	before := len(api.lists)
	if _, err := l.Load(context.Background(), r.RequestID); err == nil || strings.Contains(err.Error(), "PRIVATE") || len(api.lists) != before {
		t.Fatal("credential error leaked or triggered absence lookup")
	}
}

func TestLaunchLedgerTraversalIsBoundedAndPreservesPrefix(t *testing.T) {
	l, api, _, r, p := ledgerFixture(t)
	api.objects[l.key(r.RequestID, "", "")], _ = json.Marshal(r.Plan)
	for n := 0; n <= MaxLaunchAttempts; n++ {
		response := ledgerResponse(r, p)
		response.Attempt.FleetID = fmt.Sprintf("fleet-%08x-0000-0000-0000-000000000000", n)
		for record, value := range map[string]any{"prepared": p, "dispatch": expectedClaim(p), "response": response} {
			api.objects[l.key(r.RequestID, p.Attempt.AttemptID, record)], _ = json.Marshal(value)
		}
		r.Attempts[len(r.Attempts)-1] = response.Attempt
		r, p = ledgerSuccessor(t, r)
	}
	snapshot, err := l.Load(context.Background(), r.RequestID)
	if !errors.Is(err, ErrLaunchLedgerCorrupt) || len(snapshot.Receipt.Attempts) != MaxLaunchAttempts {
		t.Fatalf("traversal bound: attempts=%d err=%v", len(snapshot.Receipt.Attempts), err)
	}
}
