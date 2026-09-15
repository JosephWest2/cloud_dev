package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Keep real shared-ledger recovery and public up orchestration, including
// readiness refresh, while returning a controlled partial exact-ID response.
type expiryPartialInventory struct {
	*batchRunEC2
	target string
}

func (a *expiryPartialInventory) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	out, err := a.batchRunEC2.DescribeInstances(ctx, in, opts...)
	if err == nil && len(in.InstanceIds) == 1 && in.InstanceIds[0] == a.target {
		return out, errors.New("controlled partial observation")
	}
	return out, err
}

func TestExpiryTerminalResumePublicDiagnostics(t *testing.T) {
	for _, state := range []types.InstanceStateName{types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated} {
		for _, kind := range []string{"missing", "invalid", "empty", "nil", "noncanonical", "duplicate", "conflicting-duplicate", "nil-duplicate", "original", "future", "scope", "name"} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/partial=%t", state, kind, partial), func(t *testing.T) {
					path, deps, api, _, selection := batchRunFixture(t)
					deps.Clock = fixedClock{expiryTime(t, "2026-09-14T00:00:00Z")}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					first := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
					if first == nil || !first.OK || len(first.Workers) != 2 {
						t.Fatalf("initial launch: %+v", first)
					}
					originalPlan := first.Plan
					originalDigest := originalPlan.Digest()
					deadline := originalPlan.ExpiresAt
					deps.Clock = fixedClock{expiryTime(t, deadline)}
					id := first.Workers[0].ID
					wantStatus, wantExpiry := "expired", deadline
					var extra []types.Tag
					add := func(value *string) { extra = append(extra, types.Tag{Key: aws.String("ExpiresAt"), Value: value}) }
					switch kind {
					case "missing":
						wantStatus, wantExpiry = "missing", ""
					case "invalid":
						wantStatus, wantExpiry = "invalid", ""
						add(aws.String("PRIVATE invalid"))
					case "empty":
						wantStatus, wantExpiry = "invalid", ""
						add(aws.String(""))
					case "nil":
						wantStatus, wantExpiry = "invalid", ""
						add(nil)
					case "noncanonical":
						wantStatus, wantExpiry = "invalid", ""
						add(aws.String("2026-09-14T02:00:00+00:00"))
					case "duplicate":
						wantStatus, wantExpiry = "duplicate", ""
						add(&deadline)
						add(&deadline)
					case "conflicting-duplicate":
						wantStatus, wantExpiry = "duplicate", ""
						add(&deadline)
						add(aws.String("PRIVATE invalid"))
					case "nil-duplicate":
						wantStatus, wantExpiry = "duplicate", ""
						add(nil)
						add(nil)
					case "future":
						wantStatus, wantExpiry = "future", "2026-09-14T03:00:00Z"
						add(&wantExpiry)
					default:
						add(&deadline)
					}
					api.mu.Lock()
					for workerID, instance := range api.instances {
						instance.State = &types.InstanceState{Name: state}
						if workerID == id {
							tags := make([]types.Tag, 0, len(instance.Tags))
							for _, tag := range instance.Tags {
								key := aws.ToString(tag.Key)
								if key == "ExpiresAt" {
									continue
								}
								if kind == "scope" && key == "Owner" {
									tag.Value = aws.String("foreign-owner")
								}
								if kind == "name" && key == "Name" {
									tag.Value = aws.String("different-name")
								}
								tags = append(tags, tag)
							}
							instance.Tags = append(tags, extra...)
						}
						api.instances[workerID] = instance
					}
					api.mu.Unlock()
					c, err := config.Load(path, config.Overrides{})
					if err != nil {
						t.Fatal(err)
					}
					factory := deps.New
					service, err := factory(ctx, c)
					if err != nil {
						t.Fatal(err)
					}
					storage := service.LaunchRecords.(*launchMemoryS3)
					before, _ := json.Marshal(storage.objects)
					if partial {
						deps.New = func(ctx context.Context, c config.Config) (*Service, error) {
							service, err := factory(ctx, c)
							if err == nil {
								service.API = &expiryPartialInventory{batchRunEC2: api, target: id}
							}
							return service, err
						}
					}
					// Lose all local receipts; shared immutable records still recover every ID.
					deps.Store = &Store{Dir: t.TempDir()}
					resumed := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &LaunchSelection{Resume: first.RequestID}}, deps, io.Discard).Batch
					if ctx.Err() != nil {
						t.Fatal("recovery exceeded bounded test", ctx.Err())
					}
					if resumed == nil {
						t.Fatal("missing batch result")
					}
					encoded, err := json.Marshal(resumed)
					if err != nil {
						t.Fatal(err)
					}
					var public struct {
						ExpiresAt      string `json:"expires_at"`
						FulfilledCount int    `json:"fulfilled_count"`
						Instances      []struct {
							ID           string  `json:"instance_id"`
							ExpiresAt    *string `json:"expires_at"`
							ExpiryStatus string  `json:"expiry_status"`
						} `json:"instances"`
					}
					if err := json.Unmarshal(encoded, &public); err != nil {
						t.Fatal(err)
					}
					if public.ExpiresAt != deadline || public.FulfilledCount != 2 || len(public.Instances) != 2 || !reflect.DeepEqual(resumed.Plan, originalPlan) || resumed.Plan.Digest() != originalDigest || resumed.Attempts[0].FulfilledCount != 2 {
						t.Fatalf("request deadline/fulfillment/identities changed: %s", encoded)
					}
					if len(api.counts) != 1 {
						t.Fatal("resume allocated", api.counts)
					}
					found := false
					for _, worker := range public.Instances {
						if worker.ID != id {
							if worker.ExpiryStatus != "expired" || worker.ExpiresAt == nil || *worker.ExpiresAt != deadline {
								t.Fatal("peer diagnostic changed", string(encoded))
							}
							continue
						}
						found = true
						if worker.ExpiryStatus != wantStatus || (wantExpiry == "" && worker.ExpiresAt != nil) || (wantExpiry != "" && (worker.ExpiresAt == nil || *worker.ExpiresAt != wantExpiry)) {
							t.Fatalf("observed expiry lost: want %s/%s: %s", wantStatus, wantExpiry, encoded)
						}
					}
					if !found || bytes.Contains(encoded, []byte("PRIVATE")) {
						t.Fatal("identity missing or raw tags exposed", string(encoded))
					}
					if kind == "original" && !partial {
						if resumed.MissingCount == nil || *resumed.MissingCount != 0 {
							t.Fatal("valid historic response lost bounds", string(encoded))
						}
					} else {
						if resumed.MissingCount != nil {
							t.Fatal("partial or mismatched observation granted capacity bounds", string(encoded))
						}
						for _, worker := range resumed.Workers {
							if worker.ID == id && worker.Status == "historical" {
								t.Fatal("mismatched/partial worker marked verified historical")
							}
						}
					}
					after, _ := json.Marshal(storage.objects)
					if !bytes.Equal(before, after) {
						t.Fatal("immutable ledger records rewritten by observation")
					}
				})
			}
		}
	}
}

func TestExpiryLegacyFoldedPresenceInReceiptsAndLedger(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile("testdata/expiry-legacy/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	original := read("batch-v2")
	legacy, err := decodeBatchReceipt(original)
	if err != nil {
		t.Fatal(err)
	}
	l, storage, _, _, _ := ledgerFixture(t)
	id, attempt := legacy.RequestID, legacy.Attempts[0].AttemptID
	storage.objects[l.key(id, "", "")] = read("plan-v1")
	storage.objects[l.key(id, attempt, "prepared")] = read("prepared-v1")
	storage.objects[l.key(id, attempt, "dispatch")] = read("claim-v1")
	storage.objects[l.key(id, attempt, "response")] = read("response-worker-v1")
	baseline, err := l.Load(context.Background(), id)
	if err != nil || baseline.Receipt.PlanSHA256 != legacy.PlanSHA256 {
		t.Fatal("unchanged historic ledger failed", err)
	}
	// The long s is in the decoder's Unicode fold class for ASCII s. Exercise
	// literal UTF-8 and a JSON escape, as well as ordinary ASCII case variants.
	for _, key := range []string{"expires_at", "EXPIRES_AT", "Expires_At", "expireſ_at", `expire\u017f_at`} {
		for _, value := range []string{`""`, `null`} {
			t.Run(key+"/"+value, func(t *testing.T) {
				field := []byte(`"` + key + `":` + value + `,"schema_version":1`)
				rawPlan := bytes.Replace(read("plan-v1"), []byte(`"schema_version":1`), field, 1)
				// Prove this spelling is recognized by the Go struct decoder, rather than
				// merely rejected by unknown-field validation in the production decoder.
				type plain LaunchPlan
				var decoded plain
				probe := bytes.Replace(rawPlan, field, []byte(`"`+key+`":"probe","schema_version":1`), 1)
				if err := json.Unmarshal(probe, &decoded); err != nil || decoded.ExpiresAt != "probe" {
					t.Fatal("case does not exercise recognized field", err)
				}
				nested := bytes.Replace(original, []byte(`"plan":{"schema_version":1`), append([]byte(`"plan":{`), field...), 1)
				if _, err := decodeBatchReceipt(nested); err == nil {
					t.Fatal("legacy receipt accepted folded expiry presence")
				}
				storage.objects[l.key(id, "", "")] = rawPlan
				if _, err := l.Load(context.Background(), id); !errors.Is(err, ErrLaunchLedgerCorrupt) {
					t.Fatal("legacy permanent plan accepted folded expiry presence", err)
				}
			})
		}
	}
	storage.objects[l.key(id, "", "")] = read("plan-v1")
	restored, err := l.Load(context.Background(), id)
	if err != nil || !reflect.DeepEqual(restored, baseline) {
		t.Fatal("true historical records changed", err)
	}
}

func TestExpiryNewSchemaFoldedFieldsAndUnknownFields(t *testing.T) {
	original, err := os.ReadFile("testdata/expiry-v2/batch-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := decodeBatchReceipt(original)
	if err != nil {
		t.Fatal(err)
	}
	l, storage, _, _, _ := ledgerFixture(t)
	planBytes, _ := json.Marshal(baseline.Plan)
	for _, key := range []string{"EXPIRES_AT", "Expires_At", "expireſ_at", `expire\u017f_at`, "expires-at", "expires_at_unknown"} {
		t.Run(key, func(t *testing.T) {
			valid := strings.EqualFold(strings.ReplaceAll(key, `\u017f`, "ſ"), "expires_at")
			nested := bytes.Replace(original, []byte(`"expires_at"`), []byte(`"`+key+`"`), 1)
			got, err := decodeBatchReceipt(nested)
			if valid {
				if err != nil || !reflect.DeepEqual(got, baseline) {
					t.Fatal("valid v2 decoding changed", err)
				}
			} else if err == nil {
				t.Fatal("unknown nested field accepted")
			}
			storage.objects[l.key(baseline.RequestID, "", "")] = bytes.Replace(planBytes, []byte(`"expires_at"`), []byte(`"`+key+`"`), 1)
			snapshot, err := l.Load(context.Background(), baseline.RequestID)
			if valid {
				if err != nil || snapshot.Receipt.PlanSHA256 != baseline.PlanSHA256 {
					t.Fatal("valid v2 permanent plan changed", err)
				}
			} else if !errors.Is(err, ErrLaunchLedgerCorrupt) {
				t.Fatal("unknown permanent plan field accepted", err)
			}
		})
	}
}
