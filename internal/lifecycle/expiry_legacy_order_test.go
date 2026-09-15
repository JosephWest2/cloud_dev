package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

func TestExpiryLegacyResumeLaterEvidence(t *testing.T) {
	for _, latest := range scanExpiryCases("2026-09-14T02:00:00Z") {
		for _, phase := range []string{"name-partial", "name-success", "name-unrelated", "name-scope-invalid", "name-empty", "name-error", "name-not-found", "readiness-stopped", "readiness-stopping", "readiness-shutting-down", "readiness-terminated", "readiness-partial", "readiness-running"} {
			t.Run(latest.name+"/"+phase, func(t *testing.T) {
				path := testutil.Setup(t)
				cfg, err := config.Load(path, config.Overrides{})
				if err != nil {
					t.Fatal(err)
				}
				profile, err := config.LoadProfile("")
				if err != nil {
					t.Fatal(err)
				}
				manifest, err := config.LoadManifest(cfg.Manifest, cfg, profile)
				if err != nil {
					t.Fatal(err)
				}
				service, probe, ssm := readinessSetup(t)
				manifest.Readiness = probe.Readiness
				raw, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfg.Manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
				service.Scope = cfg
				receipt, err := newReceipt(parameters(cfg, manifest, profile, "smoke"))
				if err != nil {
					t.Fatal(err)
				}
				receipt.CreatedAt = "2026-09-14T00:00:00Z"
				receipt.State = "observed"
				receipt.InstanceIDs = []string{"i-12345678"}
				store := Store{Dir: t.TempDir()}
				if err := store.Save(receipt); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(store.Path(receipt.RequestID))
				if err != nil {
					t.Fatal(err)
				}
				// The initial AWS expiry differs from every later case, including valid
				// original/future controls. It is never written into the legacy receipt.
				originalExpiry := "2026-09-14T06:00:00Z"
				original := (scanExpiryCase{values: []*string{&originalExpiry}}).apply(launched(launchInput(receipt)))
				newest := latest.apply(original)
				reads := 0
				api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					reads++
					if reads == 1 {
						return inventory(original), nil
					}
					if reads == 2 {
						if len(in.InstanceIds) != 0 {
							t.Fatal("name check was not exercised")
						}
						switch phase {
						case "name-partial":
							return inventory(newest), errors.New("controlled partial name read")
						case "name-success":
							return inventory(newest), nil
						case "name-unrelated":
							row := newest
							row.InstanceId = aws.String("i-87654321")
							return inventory(row), errors.New("controlled unrelated name row")
						case "name-scope-invalid":
							row := latest.apply(original)
							for n, tag := range row.Tags {
								if aws.ToString(tag.Key) == "Owner" {
									row.Tags[n].Value = aws.String("foreign-owner")
								}
							}
							return inventory(row), nil
						case "name-empty":
							return inventory(), nil
						case "name-error":
							return nil, errors.New("controlled name failure")
						case "name-not-found":
							return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
						default:
							return inventory(original), nil
						}
					}
					if reads == 3 && strings.HasPrefix(phase, "readiness-") {
						if len(in.InstanceIds) != 1 || in.InstanceIds[0] != receipt.InstanceIDs[0] {
							t.Fatal("readiness was not an exact-ID lookup")
						}
						switch phase {
						case "readiness-partial":
							return inventory(newest), errors.New("controlled partial readiness read")
						case "readiness-running":
						default:
							newest.State = &types.InstanceState{Name: types.InstanceStateName(strings.TrimPrefix(phase, "readiness-"))}
						}
						return inventory(newest), nil
					}
					// A later read without a row must not erase name/readiness evidence.
					return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
				}}
				service.API = api
				deps := Dependencies{Store: &store, Clock: fixedClock{expiryTime(t, "2026-09-14T02:00:00Z")}, New: func(context.Context, config.Config) (*Service, error) { return service, nil }}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				result := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &LaunchSelection{Resume: receipt.RequestID}}, deps, io.Discard)
				if ctx.Err() != nil {
					t.Fatal("resume exceeded test deadline", ctx.Err())
				}
				if len(result.Instances) != 1 || result.Instances[0].ID != receipt.InstanceIDs[0] || api.launches != 0 || ssm.sent != 0 {
					t.Fatalf("legacy identity/allocation/probe invariant: %+v", result)
				}
				wantCode := "target_unresolved"
				switch phase {
				case "name-partial", "name-unrelated", "name-error", "name-not-found", "readiness-partial":
					wantCode = "inventory_unavailable"
				case "name-scope-invalid":
					wantCode = "scope_mismatch"
				case "readiness-stopped", "readiness-stopping", "readiness-shutting-down":
					wantCode = "instance_not_running"
				}
				if result.OK || result.Code != wantCode {
					t.Fatalf("legacy selection/error authority changed: want %s, got %+v", wantCode, result)
				}
				want := latest
				switch phase {
				case "name-unrelated", "name-scope-invalid", "name-empty", "name-error", "name-not-found":
					want = scanExpiryCase{status: "future", value: originalExpiry}
				}
				encoded, err := json.Marshal(PublicResult(result))
				if err != nil {
					t.Fatal(err)
				}
				var public struct {
					Instances []struct {
						ID        string  `json:"instance_id"`
						ExpiresAt *string `json:"expires_at"`
						Status    string  `json:"expiry_status"`
					} `json:"instances"`
				}
				if err := json.Unmarshal(encoded, &public); err != nil {
					t.Fatal(err)
				}
				worker := public.Instances[0]
				if worker.Status != want.status || (want.value == "" && worker.ExpiresAt != nil) || (want.value != "" && (worker.ExpiresAt == nil || *worker.ExpiresAt != want.value)) {
					t.Fatalf("want %s/%s after %d reads: %s", want.status, want.value, reads, encoded)
				}
				if bytes.Contains(encoded, []byte("PRIVATE")) {
					t.Fatal("raw malformed tag exposed")
				}
				after, err := os.ReadFile(store.Path(receipt.RequestID))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("legacy receipt bytes changed")
				}
				saved, err := store.Load(receipt.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				oldInput, _ := json.Marshal(launchInput(receipt))
				newInput, _ := json.Marshal(launchInput(saved))
				if !bytes.Equal(oldInput, newInput) {
					t.Fatal("historical launch serializer or token changed")
				}
			})
		}
	}
}

// Preserve EC2's ordering both within one response and across pages. All rows
// deliberately duplicate the same ID, so diagnostic precedence cannot grant
// allocation authority even when the final expiry is valid.
type expiryOrderedExactInventory struct {
	*batchRunEC2
	target   string
	terminal types.InstanceStateName
	sequence string
	latest   scanExpiryCase
	layout   string
}

func (a *expiryOrderedExactInventory) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	out, err := a.batchRunEC2.DescribeInstances(ctx, in, opts...)
	if out == nil || len(in.InstanceIds) != 1 || in.InstanceIds[0] != a.target {
		return out, err
	}
	for n, reservation := range out.Reservations {
		rows := []types.Instance{}
		for _, base := range reservation.Instances {
			for j, state := range a.sequence {
				row := base
				if state == 't' {
					row.State = &types.InstanceState{Name: a.terminal}
				} else {
					row.State = &types.InstanceState{Name: types.InstanceStateNameRunning}
				}
				if j == len(a.sequence)-1 {
					row = a.latest.apply(row)
				}
				rows = append(rows, row)
			}
		}
		if a.layout == "pages" || a.layout == "partial-pages" {
			if in.NextToken == nil {
				rows = rows[:len(rows)-1]
				out.NextToken = aws.String("expiry-final-row")
			} else {
				rows = rows[len(rows)-1:]
				out.NextToken = nil
			}
		}
		out.Reservations[n].Instances = rows
	}
	if a.layout == "partial" || (a.layout == "partial-pages" && in.NextToken != nil) {
		err = errors.New("controlled partial ordered exact response")
	}
	return out, err
}

func TestExpiryPublicMixedExactObservationOrder(t *testing.T) {
	for _, terminal := range []types.InstanceStateName{types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated} {
		for _, sequence := range []string{"tl", "lt", "tlt", "ltl"} {
			for _, latest := range scanExpiryCases("2026-09-14T02:00:00Z") {
				for _, layout := range []string{"one-page", "pages", "partial", "partial-pages"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", terminal, sequence, latest.name, layout), func(t *testing.T) {
						path, deps, api, _, selection := batchRunFixture(t)
						deps.Clock = fixedClock{expiryTime(t, "2026-09-14T00:00:00Z")}
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						first := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &selection}, deps, io.Discard).Batch
						if first == nil || !first.OK {
							t.Fatalf("initial launch: %+v", first)
						}
						cfg, err := config.Load(path, config.Overrides{})
						if err != nil {
							t.Fatal(err)
						}
						factory := deps.New
						svc, err := factory(ctx, cfg)
						if err != nil {
							t.Fatal(err)
						}
						storage := svc.LaunchRecords.(*launchMemoryS3)
						before, err := json.Marshal(storage.objects)
						if err != nil {
							t.Fatal(err)
						}
						id := first.Workers[0].ID
						deps.New = func(ctx context.Context, c config.Config) (*Service, error) {
							s, err := factory(ctx, c)
							if err == nil {
								s.API = &expiryOrderedExactInventory{batchRunEC2: api, target: id, terminal: terminal, sequence: sequence, latest: latest, layout: layout}
							}
							return s, err
						}
						deps.Clock = fixedClock{expiryTime(t, first.Plan.ExpiresAt)}
						deps.Store = &Store{Dir: t.TempDir()}
						result := Run(ctx, path, config.Overrides{}, Options{Command: "up", Selection: &LaunchSelection{Resume: first.RequestID}}, deps, io.Discard).Batch
						if ctx.Err() != nil {
							t.Fatal("resume exceeded test deadline", ctx.Err())
						}
						if result == nil {
							t.Fatal("missing public result")
						}
						assertScanExpiryResult(t, result, first.Plan, id, latest, 2)
						if result.OK || result.MissingCount != nil || len(api.counts) != 1 || result.Workers[1].ID != first.Workers[1].ID {
							t.Fatal("duplicate observation granted authority or lost peer")
						}
						duplicate := false
						for _, problem := range result.Errors {
							duplicate = duplicate || (problem.ResourceID == id && problem.Code == "worker_inventory_invalid")
						}
						if !duplicate {
							t.Fatal("duplicate identity error lost")
						}
						after, err := json.Marshal(storage.objects)
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(before, after) {
							t.Fatal("ordered observations rewrote immutable ledger")
						}
					})
				}
			}
		}
	}
}
