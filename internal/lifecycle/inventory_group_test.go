package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func inventoryBatchWorker(id, base, group, request, parent string) types.Instance {
	i := worker(id)
	attempt, _ := AttemptID(request, parent)
	for key, value := range map[string]string{
		"Name": base, "BaseName": base, "NamingVersion": "1", "Profile": "agent",
		"Group": group, "RequestId": request, "BatchId": request, "AttemptId": attempt,
		"CreatedAt": "2026-09-14T12:00:00Z", "aws:ec2launchtemplate:id": "lt-0123456789abcdef0", "aws:ec2launchtemplate:version": "4",
	} {
		inventorySetTag(&i, key, value)
	}
	i.SubnetId = aws.String("subnet-0123456789abcdef0")
	i.Placement = &types.Placement{AvailabilityZone: aws.String("us-east-2a")}
	i.InstanceLifecycle = types.InstanceLifecycleTypeSpot
	return i
}

func inventorySetTag(i *types.Instance, key, value string) {
	for n := range i.Tags {
		if aws.ToString(i.Tags[n].Key) == key {
			i.Tags[n].Value = aws.String(value)
			return
		}
	}
	i.Tags = append(i.Tags, types.Tag{Key: aws.String(key), Value: aws.String(value)})
}

func inventoryDeleteTag(i *types.Instance, key string) {
	for n := range i.Tags {
		if aws.ToString(i.Tags[n].Key) == key {
			i.Tags = append(i.Tags[:n], i.Tags[n+1:]...)
			return
		}
	}
}

func requireInventoryFilters(t *testing.T, in *ec2.DescribeInstancesInput, extra map[string]string) {
	t.Helper()
	want := map[string][]string{"tag:ManagedBy": {"devbox"}, "tag:Deployment": {"test"}, "tag:Owner": {"test-owner"}}
	for key, value := range extra {
		want[key] = []string{value}
	}
	got := map[string][]string{}
	for _, filter := range in.Filters {
		key := aws.ToString(filter.Name)
		if _, duplicate := got[key]; duplicate {
			t.Fatalf("duplicate filter %s", key)
		}
		got[key] = filter.Values
	}
	if len(in.InstanceIds) != 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("filters=%v IDs=%v want=%v", got, in.InstanceIds, want)
	}
}

func inventoryIDs(found []Instance) []string {
	ids := []string{}
	for _, instance := range found {
		ids = append(ids, instance.ID)
	}
	return ids
}

func requireInventoryFailure(t *testing.T, err error, code string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("err=%v want code %s", err, code)
	}
}

func TestGroupInventoryCloudOnlyPaginationAndIndependentRequests(t *testing.T) {
	requestA, requestB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	first := inventoryBatchWorker("i-0123456789abcdef0", "workers", "research", requestA, "")
	second := inventoryBatchWorker("i-0123456789abcdef1", "workers", "research", requestA, tagsOf(first)["AttemptId"])
	third := inventoryBatchWorker("i-0123456789abcdef2", "workers", "research", requestB, "")
	calls := 0
	api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		requireInventoryFilters(t, in, map[string]string{"tag:Group": "research"})
		calls++
		switch aws.ToString(in.NextToken) {
		case "":
			out := inventory(third, first)
			out.NextToken = aws.String("next")
			return out, nil
		case "next":
			return inventory(first, second), nil
		default:
			t.Fatalf("unexpected pagination %q", aws.ToString(in.NextToken))
			return nil, nil
		}
	}}
	// A new service has no receipt cache. Everything needed for rediscovery is
	// supplied by AWS, including separate attempts in one shared group.
	found, err := testService(api).ListGroup(context.Background(), "research")
	wantIDs := []string{"i-0123456789abcdef0", "i-0123456789abcdef1", "i-0123456789abcdef2"}
	if err != nil || calls != 2 || !reflect.DeepEqual(inventoryIDs(found), wantIDs) {
		t.Fatalf("found=%+v err=%v calls=%d", found, err, calls)
	}
	for n, item := range found {
		name, _ := WorkerName("workers", wantIDs[n])
		if item.Name != name || item.BaseName != "workers" || item.Group != "research" || item.AttemptID == "" || item.SubnetID != "subnet-0123456789abcdef0" || item.AvailabilityZone != "us-east-2a" || item.Image != "ami-12345678" || item.Type != "c7i.2xlarge" || item.Market != "spot" || item.TemplateID != "lt-0123456789abcdef0" || item.TemplateVersion != "4" || item.State != "running" || item.SSM != "not_observed" || item.Readiness != "not_observed" {
			t.Fatalf("missing cloud identity %+v", item)
		}
	}
	if found[0].RequestID != requestA || found[1].RequestID != requestA || found[2].RequestID != requestB || found[0].AttemptID == found[1].AttemptID {
		t.Fatalf("group merged independent request/attempt identities: %+v", found)
	}
	if api.launches != 0 || api.terminations != 0 {
		t.Fatal("inventory mutated cloud resources")
	}
}

func TestGeneratedNamesResolveAfterRestartAndPeerLoss(t *testing.T) {
	base := strings.Repeat("w", 63)
	selected := inventoryBatchWorker("i-0123456789abcdef0", base, "research", strings.Repeat("a", 32), "")
	peer := inventoryBatchWorker("i-0123456789abcdef1", base, "research", strings.Repeat("b", 32), "")
	name, _ := WorkerName(base, aws.ToString(selected.InstanceId))
	for _, items := range [][]types.Instance{{peer, selected}, {selected}} {
		api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			requireInventoryFilters(t, in, nil)
			return inventory(items...), nil
		}}
		found, err := testService(api).Resolve(context.Background(), name)
		if err != nil || len(found) != 1 || found[0].ID != aws.ToString(selected.InstanceId) || found[0].Name != name || len(name) != 63 {
			t.Fatalf("name=%s found=%+v err=%v", name, found, err)
		}
	}
	api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		requireInventoryFilters(t, in, nil)
		return inventory(selected), nil
	}}
	wrongName := "wrong-i-0123456789abcdef0"
	found, err := testService(api).Resolve(context.Background(), wrongName)
	requireInventoryFailure(t, err, "target_unresolved")
	if len(found) != 0 {
		t.Fatalf("suffix alone resolved incorrect base: %+v", found)
	}
}

func TestGeneratedNameCollisionWithLegacyRetainsCandidates(t *testing.T) {
	batch := inventoryBatchWorker("i-0123456789abcdef0", "workers", "research", strings.Repeat("a", 32), "")
	name, _ := WorkerName("workers", aws.ToString(batch.InstanceId))
	legacy := worker("i-0123456789abcdef1")
	inventorySetTag(&legacy, "Name", name)
	api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		if len(in.InstanceIds) == 1 {
			if in.InstanceIds[0] != aws.ToString(batch.InstanceId) || len(in.Filters) != 0 {
				t.Fatalf("explicit ID input %+v", in)
			}
			return inventory(batch), nil
		}
		requireInventoryFilters(t, in, nil)
		return inventory(legacy, batch), nil
	}}
	svc := testService(api)
	found, err := svc.Resolve(context.Background(), name)
	requireInventoryFailure(t, err, "name_ambiguous")
	if !reflect.DeepEqual(inventoryIDs(found), []string{aws.ToString(batch.InstanceId), aws.ToString(legacy.InstanceId)}) {
		t.Fatalf("missing ambiguity candidates %+v", found)
	}
	found, err = svc.Resolve(context.Background(), aws.ToString(batch.InstanceId))
	if err != nil || len(found) != 1 || found[0].ID != aws.ToString(batch.InstanceId) {
		t.Fatalf("explicit ID did not disambiguate: %+v %v", found, err)
	}
}

func TestLegacyNamesRemainDiscoverableAndBatchBaseDoesNotResolve(t *testing.T) {
	legacy := worker("i-12345678")
	batch := inventoryBatchWorker("i-0123456789abcdef0", "smoke", "research", strings.Repeat("a", 32), "")
	api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		requireInventoryFilters(t, in, map[string]string{"tag:Name": "smoke"})
		return inventory(batch, legacy), nil
	}}
	found, err := testService(api).Resolve(context.Background(), "smoke")
	if err != nil || len(found) != 1 || found[0].ID != "i-12345678" || found[0].Name != "smoke" || found[0].Group != "" {
		t.Fatalf("legacy name changed: %+v %v", found, err)
	}
	api.describe = func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		requireInventoryFilters(t, in, map[string]string{"tag:Name": "smoke"})
		return inventory(batch), nil
	}
	found, err = testService(api).Resolve(context.Background(), "smoke")
	requireInventoryFailure(t, err, "target_unresolved")
	if len(found) != 0 {
		t.Fatal("shared base accidentally selected a worker")
	}
	api.describe = func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		requireInventoryFilters(t, in, nil)
		return inventory(legacy), nil
	}
	found, err = testService(api).List(context.Background())
	if err != nil || len(found) != 1 || found[0].Group != "" {
		t.Fatalf("legacy worker missing from unfiltered list: %+v %v", found, err)
	}
}

func TestGroupInventoryRejectsMalformedCloudIdentity(t *testing.T) {
	mutations := map[string]func(*types.Instance){
		"duplicate scope": func(i *types.Instance) { i.Tags = append(i.Tags, i.Tags[0]) },
		"conflicting scope": func(i *types.Instance) {
			i.Tags = append(i.Tags, types.Tag{Key: aws.String("Owner"), Value: aws.String("other")})
		},
		"nil key":             func(i *types.Instance) { i.Tags = append(i.Tags, types.Tag{Value: aws.String("value")}) },
		"nil value":           func(i *types.Instance) { i.Tags = append(i.Tags, types.Tag{Key: aws.String("unexpected")}) },
		"unknown schema":      func(i *types.Instance) { inventorySetTag(i, "NamingVersion", "2") },
		"empty schema":        func(i *types.Instance) { inventorySetTag(i, "NamingVersion", "") },
		"missing schema":      func(i *types.Instance) { inventoryDeleteTag(i, "NamingVersion") },
		"missing base":        func(i *types.Instance) { inventoryDeleteTag(i, "BaseName") },
		"changed Name":        func(i *types.Instance) { inventorySetTag(i, "Name", "other") },
		"invalid base":        func(i *types.Instance) { inventorySetTag(i, "BaseName", "PRIVATE\nDATA") },
		"invalid group":       func(i *types.Instance) { inventorySetTag(i, "Group", "PRIVATE\nDATA") },
		"invalid request":     func(i *types.Instance) { inventorySetTag(i, "RequestId", "PRIVATE\nDATA") },
		"batch conflict":      func(i *types.Instance) { inventorySetTag(i, "BatchId", strings.Repeat("b", 32)) },
		"invalid attempt":     func(i *types.Instance) { inventorySetTag(i, "AttemptId", "PRIVATE\nDATA") },
		"missing profile":     func(i *types.Instance) { inventoryDeleteTag(i, "Profile") },
		"invalid creation":    func(i *types.Instance) { inventorySetTag(i, "CreatedAt", "PRIVATE\nDATA") },
		"floating template":   func(i *types.Instance) { inventorySetTag(i, "aws:ec2launchtemplate:version", "$Latest") },
		"template wrong kind": func(i *types.Instance) { inventorySetTag(i, "aws:ec2launchtemplate:id", "ami-12345678") },
		"image wrong kind":    func(i *types.Instance) { i.ImageId = aws.String("lt-12345678") },
		"missing subnet":      func(i *types.Instance) { i.SubnetId = nil },
		"missing zone":        func(i *types.Instance) { i.Placement = nil },
		"foreign zone":        func(i *types.Instance) { i.Placement.AvailabilityZone = aws.String("us-east-1a") },
		"unsupported market":  func(i *types.Instance) { i.InstanceLifecycle = types.InstanceLifecycleTypeScheduled },
		"invalid ID":          func(i *types.Instance) { i.InstanceId = aws.String("i-PRIVATE") },
		"invalid state":       func(i *types.Instance) { i.State.Name = "PRIVATE\nDATA" },
	}
	for label, mutate := range mutations {
		t.Run(label, func(t *testing.T) {
			i := inventoryBatchWorker("i-0123456789abcdef0", "workers", "research", strings.Repeat("a", 32), "")
			mutate(&i)
			api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return inventory(i), nil }}
			found, err := testService(api).ListGroup(context.Background(), "research")
			requireInventoryFailure(t, err, "inventory_invalid")
			encoded, _ := json.Marshal(found)
			if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(err.Error(), "PRIVATE") || api.launches != 0 || api.terminations != 0 {
				t.Fatalf("unsafe malformed response found=%s err=%v", encoded, err)
			}
		})
	}
}

func TestGroupInventoryRevalidatesFiltersAndScope(t *testing.T) {
	for _, scenario := range []string{"account", "owner", "deployment", "managed", "group", "request", "name", "id"} {
		t.Run(scenario, func(t *testing.T) {
			i := inventoryBatchWorker("i-0123456789abcdef0", "workers", "research", strings.Repeat("a", 32), "")
			out := inventory(i)
			code := "inventory_invalid"
			switch scenario {
			case "account":
				out.Reservations[0].OwnerId = aws.String("999999999999")
				code = "scope_mismatch"
			case "owner", "deployment", "managed":
				key := map[string]string{"owner": "Owner", "deployment": "Deployment", "managed": "ManagedBy"}[scenario]
				inventorySetTag(&out.Reservations[0].Instances[0], key, "other")
				code = "scope_mismatch"
			}
			api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) { return out, nil }}
			svc := testService(api)
			var found []Instance
			var err error
			switch scenario {
			case "request":
				found, err = svc.inventory(context.Background(), "", "", strings.Repeat("b", 32))
			case "name":
				found, err = svc.Resolve(context.Background(), "other")
			case "id":
				found, err = svc.Resolve(context.Background(), "i-0123456789abcdef1")
			default:
				found, err = svc.ListGroup(context.Background(), "other")
			}
			requireInventoryFailure(t, err, code)
			if len(found) != 0 {
				t.Fatalf("unexpected identity accepted: %+v", found)
			}
		})
	}
}

func TestInventoryKeepsSortedKnownIDsOnPartialReadFailure(t *testing.T) {
	for _, scenario := range []string{"next page error", "partial page error", "pagination cycle", "cancellation", "malformed peer"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			api := &fakeEC2{describe: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				calls++
				if calls == 1 {
					out := inventory(worker("i-87654321"))
					out.NextToken = aws.String("next")
					return out, nil
				}
				if aws.ToString(in.NextToken) != "next" {
					t.Fatalf("lost continuation %q", aws.ToString(in.NextToken))
				}
				out := inventory(worker("i-12345678"))
				switch scenario {
				case "next page error":
					return nil, errors.New("PRIVATE transport detail")
				case "partial page error":
					return out, errors.New("PRIVATE transport detail")
				case "pagination cycle":
					out.NextToken = aws.String("next")
				case "cancellation":
					cancel()
					return out, ctx.Err()
				case "malformed peer":
					bad := worker("i-PRIVATE")
					out = inventory(bad, worker("i-12345678"))
				}
				return out, nil
			}}
			found, err := testService(api).List(ctx)
			want := []string{"i-12345678", "i-87654321"}
			if scenario == "next page error" {
				want = []string{"i-87654321"}
			}
			if err == nil || strings.Contains(err.Error(), "PRIVATE") || !reflect.DeepEqual(inventoryIDs(found), want) || calls != 2 {
				t.Fatalf("found=%+v err=%v calls=%d", found, err, calls)
			}
		})
	}
}

func TestInventoryRejectsConflictingDuplicateEvenOutsideNameSelection(t *testing.T) {
	i := inventoryBatchWorker("i-0123456789abcdef0", "workers", "research", strings.Repeat("a", 32), "")
	name, _ := WorkerName("workers", aws.ToString(i.InstanceId))
	for _, reverse := range []bool{false, true} {
		calls := 0
		api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			calls++
			current := inventoryBatchWorker("i-0123456789abcdef0", "workers", "research", strings.Repeat("a", 32), "")
			if (calls == 1) == reverse {
				inventorySetTag(&current, "Name", "other")
				inventorySetTag(&current, "BaseName", "other")
			}
			out := inventory(current)
			if calls == 1 {
				out.NextToken = aws.String("next")
			}
			return out, nil
		}}
		found, err := testService(api).Resolve(context.Background(), name)
		requireInventoryFailure(t, err, "inventory_invalid")
		if len(found) != 1 || found[0].ID != aws.ToString(i.InstanceId) {
			t.Fatalf("conflict dropped known target: %+v", found)
		}
	}
}

func TestGroupValidationHappensBeforeAWS(t *testing.T) {
	api := &fakeEC2{describe: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		t.Fatal("invalid group reached AWS")
		return nil, nil
	}}
	for _, group := range []string{"", "contains space", "i-12345678", strings.Repeat("a", 64)} {
		_, err := testService(api).ListGroup(context.Background(), group)
		requireInventoryFailure(t, err, "group_invalid")
	}
}

func TestWorkerOutcomeJSONUsesEmbeddedInventoryIdentity(t *testing.T) {
	i := record(inventoryBatchWorker("i-0123456789abcdef0", "workers", "research", strings.Repeat("a", 32), ""))
	encoded, err := json.Marshal(WorkerOutcome{Instance: i, Status: "allocated"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]string{"group": i.Group, "base_name": i.BaseName, "attempt_id": i.AttemptID, "subnet_id": i.SubnetID, "availability_zone": i.AvailabilityZone} {
		if fields[field] != value {
			t.Fatalf("embedded %s hidden: %s", field, encoded)
		}
	}
}
