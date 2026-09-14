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
	"github.com/aws/smithy-go"
)

type reconcileFleetPage struct {
	out *ec2.DescribeFleetsOutput
	err error
}

type reconcileInventory struct {
	account     string
	scanPages   []fleetInstancesPage
	scanInputs  []ec2.DescribeInstancesInput
	instances   map[string]types.Instance
	exactErrors map[string]error
	exactPages  map[string][]fleetInstancesPage
	exactCounts map[string]int
	exactInputs []ec2.DescribeInstancesInput
	volumes     map[string]types.Volume
	volumeError error
	fleetPages  []reconcileFleetPage
	fleetInputs []ec2.DescribeFleetsInput
}

func (s *reconcileInventory) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	copy := *in
	if in.NextToken != nil {
		copy.NextToken = aws.String(*in.NextToken)
	}
	if len(in.InstanceIds) == 0 {
		n := len(s.scanInputs)
		s.scanInputs = append(s.scanInputs, copy)
		if n >= len(s.scanPages) {
			return nil, errors.New("unexpected scan")
		}
		return s.scanPages[n].out, s.scanPages[n].err
	}
	s.exactInputs = append(s.exactInputs, copy)
	if len(in.InstanceIds) != 1 || len(in.Filters) != 0 {
		return nil, errors.New("exact ID request must isolate each worker")
	}
	id := in.InstanceIds[0]
	if pages, exists := s.exactPages[id]; exists {
		if s.exactCounts == nil {
			s.exactCounts = map[string]int{}
		}
		n := s.exactCounts[id]
		s.exactCounts[id]++
		if n >= len(pages) {
			return nil, errors.New("unexpected exact page")
		}
		return pages[n].out, pages[n].err
	}
	if err := s.exactErrors[id]; err != nil {
		return nil, err
	}
	if instance, exists := s.instances[id]; exists {
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(s.account), Instances: []types.Instance{instance}}}}, nil
	}
	return &ec2.DescribeInstancesOutput{}, nil
}

func (s *reconcileInventory) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if s.volumeError != nil {
		return nil, s.volumeError
	}
	out := &ec2.DescribeVolumesOutput{Volumes: []types.Volume{}}
	for _, id := range in.VolumeIds {
		if volume, exists := s.volumes[id]; exists {
			out.Volumes = append(out.Volumes, volume)
		}
	}
	return out, nil
}

func (s *reconcileInventory) DescribeFleets(_ context.Context, in *ec2.DescribeFleetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeFleetsOutput, error) {
	copy := *in
	if in.NextToken != nil {
		copy.NextToken = aws.String(*in.NextToken)
	}
	n := len(s.fleetInputs)
	s.fleetInputs = append(s.fleetInputs, copy)
	if n >= len(s.fleetPages) {
		return &ec2.DescribeFleetsOutput{}, nil
	}
	return s.fleetPages[n].out, s.fleetPages[n].err
}

func reconcileFixture(t *testing.T, state string, recorded, observed int) (LaunchSnapshot, *reconcileInventory) {
	t.Helper()
	plan, attempt, workers, inventory := fleetWorkersFixture(t, false)
	attempt.State, attempt.FleetID = state, fleetWireID
	if state == "rejected" {
		attempt.FleetID = ""
	}
	attempt.InstanceIDs = append([]string{}, attempt.InstanceIDs[:recorded]...)
	attempt.Errors = []ResourceError{}
	if recorded < attempt.RequestedCount {
		diagnostic, _ := fleetDiagnostic("InsufficientInstanceCapacity")
		attempt.Errors = []ResourceError{diagnostic}
	}
	preparedAttempt := attempt
	preparedAttempt.State, preparedAttempt.FleetID, preparedAttempt.InstanceIDs, preparedAttempt.Errors = "prepared", "", []string{}, []ResourceError{}
	input, err := BuildFleetInput(plan, preparedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	prepared := PreparedAttempt{SchemaVersion: 1, RequestID: plan.RequestID, PlanSHA256: plan.Digest(), InputSHA256: fleetInputDigest(input), Attempt: preparedAttempt}
	response := AttemptResponse{SchemaVersion: 1, RequestID: plan.RequestID, PlanSHA256: plan.Digest(), InputSHA256: prepared.InputSHA256, Attempt: attempt, Workers: append([]WorkerOutcome{}, workers[:recorded]...)}
	snapshot := LaunchSnapshot{
		Receipt:  BatchReceipt{SchemaVersion: 2, RequestID: plan.RequestID, Plan: plan, PlanSHA256: plan.Digest(), Attempts: []AttemptReceipt{attempt}},
		Prepared: map[string]PreparedAttempt{attempt.AttemptID: prepared}, Claims: map[string]DispatchClaim{attempt.AttemptID: expectedClaim(prepared)}, Responses: map[string]AttemptResponse{attempt.AttemptID: response},
	}
	api := &reconcileInventory{account: plan.Account, instances: map[string]types.Instance{}, exactErrors: map[string]error{}, volumes: map[string]types.Volume{}}
	instances := inventory.instances[0].out.Reservations[0].Instances[:observed]
	for _, instance := range instances {
		api.instances[aws.ToString(instance.InstanceId)] = instance
	}
	for _, volume := range inventory.volumes[0].out.Volumes[:observed] {
		api.volumes[aws.ToString(volume.VolumeId)] = volume
	}
	api.scanPages = []fleetInstancesPage{{out: &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(plan.Account), Instances: instances}}}}}
	return snapshot, api
}

func reconcileFleetFixture(t *testing.T, snapshot LaunchSnapshot) types.FleetData {
	t.Helper()
	attempt := snapshot.Receipt.Attempts[0]
	input, err := BuildFleetInput(snapshot.Receipt.Plan, snapshot.Prepared[attempt.AttemptID].Attempt)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	var fleet types.FleetData
	if err := json.Unmarshal(encoded, &fleet); err != nil {
		t.Fatal(err)
	}
	fleet.FleetId = aws.String(attempt.FleetID)
	fleet.Tags = input.TagSpecifications[0].Tags
	fleet.ActivityStatus = types.FleetActivityStatusFulfilled
	for _, worker := range snapshot.Responses[attempt.AttemptID].Workers {
		for _, override := range fleet.LaunchTemplateConfigs[0].Overrides {
			if string(override.InstanceType) == worker.Type && aws.ToString(override.SubnetId) == worker.SubnetID {
				o := override
				fleet.Instances = append(fleet.Instances, types.DescribeFleetsInstances{InstanceIds: []string{worker.ID}, InstanceType: types.InstanceType(worker.Type), Lifecycle: types.InstanceLifecycle(snapshot.Receipt.Plan.Market), LaunchTemplateAndOverrides: &types.LaunchTemplateAndOverridesResponse{LaunchTemplateSpecification: fleet.LaunchTemplateConfigs[0].LaunchTemplateSpecification, Overrides: &o}})
			}
		}
	}
	return fleet
}

func TestReconcileLaunchOnlySharedResponseBoundsAllocation(t *testing.T) {
	for _, test := range []struct {
		name               string
		state              string
		recorded, observed int
		bounded            bool
	}{
		{"one-of-two", "complete", 1, 1, true},
		{"zero-of-two", "complete", 0, 0, true},
		{"definitive-rejection", "rejected", 0, 0, true},
		{"all-two", "complete", 2, 2, true},
		{"unknown-one", "unknown", 1, 1, false},
		{"unknown-discovers-second", "unknown", 1, 2, false},
		{"unknown-empty-scan", "unknown", 0, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, api := reconcileFixture(t, test.state, test.recorded, test.observed)
			before, _ := json.Marshal(snapshot)
			out, err := ReconcileLaunch(context.Background(), api, snapshot)
			if err != nil || out.Bounded != test.bounded || len(out.Workers) != test.observed || len(out.Receipt.Attempts[0].InstanceIDs) != test.observed {
				t.Fatalf("bounded=%v workers=%+v err=%v", out.Bounded, out.Workers, err)
			}
			if test.state == "unknown" && out.Receipt.Attempts[0].State != "unknown" {
				t.Fatal("inventory promoted an unknown response")
			}
			for _, worker := range out.Workers {
				if worker.Status != "allocated" || len(worker.Volumes) != 1 {
					t.Fatalf("known worker was not independently verified: %+v", worker)
				}
			}
			want := map[string][]string{"tag:ManagedBy": {"devbox"}, "tag:Deployment": {snapshot.Receipt.Plan.Deployment}, "tag:Owner": {snapshot.Receipt.Plan.Owner}, "tag:RequestId": {snapshot.Receipt.RequestID}}
			filters := map[string][]string{}
			for _, filter := range api.scanInputs[0].Filters {
				filters[aws.ToString(filter.Name)] = filter.Values
			}
			if !reflect.DeepEqual(filters, want) || len(api.scanInputs[0].InstanceIds) != 0 {
				t.Fatalf("request scan scope changed: %+v", api.scanInputs[0])
			}
			for _, input := range api.fleetInputs {
				if !reflect.DeepEqual(input.FleetIds, []string{fleetWireID}) || len(input.Filters) != 0 {
					t.Fatalf("Fleet was not queried by exact known ID: %+v", input)
				}
			}
			after, _ := json.Marshal(snapshot)
			if string(before) != string(after) {
				t.Fatal("reconciliation mutated shared evidence")
			}
		})
	}
}

func TestReconcileLaunchPaginationAndReadFailuresPreserveIDs(t *testing.T) {
	for _, mode := range []string{"pages", "cycle", "partial-error", "unavailable", "delayed-scan", "volume-error"} {
		t.Run(mode, func(t *testing.T) {
			snapshot, api := reconcileFixture(t, "complete", 2, 2)
			instances := api.scanPages[0].out.Reservations[0].Instances
			page := func(values []types.Instance, token string) *ec2.DescribeInstancesOutput {
				return &ec2.DescribeInstancesOutput{NextToken: aws.String(token), Reservations: []types.Reservation{{OwnerId: aws.String(api.account), Instances: values}}}
			}
			api.scanPages = []fleetInstancesPage{{out: page(instances[:1], "next")}, {out: page(instances[1:], "")}}
			switch mode {
			case "cycle":
				api.scanPages[1].out.NextToken = aws.String("next")
			case "partial-error":
				api.scanPages[1].err = errors.New("secret SDK diagnostics")
			case "unavailable":
				api.scanPages = []fleetInstancesPage{{err: errors.New("secret SDK diagnostics")}}
			case "delayed-scan":
				api.scanPages = []fleetInstancesPage{{out: &ec2.DescribeInstancesOutput{}}}
			case "volume-error":
				api.volumeError = errors.New("secret SDK diagnostics")
			}
			out, err := ReconcileLaunch(context.Background(), api, snapshot)
			wantBounded := mode == "pages" || mode == "delayed-scan"
			if out.Bounded != wantBounded || (err == nil) != wantBounded || len(out.Workers) != 2 || len(out.Receipt.FulfilledIDs()) != 2 {
				t.Fatalf("lost bound or IDs: %+v err=%v", out, err)
			}
			for _, worker := range out.Workers {
				if len(worker.Volumes) != 1 {
					t.Fatalf("read failure lost root mapping: %+v", worker)
				}
			}
			encoded, _ := json.Marshal(out)
			if strings.Contains(string(encoded), "secret") || err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("raw SDK diagnostics escaped")
			}
			if len(api.scanInputs) > 1 && aws.ToString(api.scanInputs[1].NextToken) != "next" {
				t.Fatal("scan did not follow pagination")
			}
		})
	}
}

func TestReconcileLaunchConflictingWorkersBlockRetry(t *testing.T) {
	for _, mode := range []string{"owner", "scope", "request", "unknown-attempt", "image", "template", "market", "placement", "duplicate", "extra-worker", "root-delete"} {
		t.Run(mode, func(t *testing.T) {
			snapshot, api := reconcileFixture(t, "complete", 1, 1)
			id := snapshot.Receipt.Attempts[0].InstanceIDs[0]
			instance := api.instances[id]
			// Isolate the scan copy: a contradictory page remains an error even
			// when a later exact read would otherwise pass.
			encoded, _ := json.Marshal(instance)
			_ = json.Unmarshal(encoded, &instance)
			switch mode {
			case "owner":
				api.scanPages[0].out.Reservations[0].OwnerId = aws.String("999999999999")
			case "scope":
				changeFleetTestTag(instance.Tags, "Owner", "different")
			case "request":
				changeFleetTestTag(instance.Tags, "RequestId", strings.Repeat("b", 32))
			case "unknown-attempt":
				changeFleetTestTag(instance.Tags, "AttemptId", strings.Repeat("b", 32))
			case "image":
				instance.ImageId = aws.String("ami-ffffffff")
			case "template":
				changeFleetTestTag(instance.Tags, "aws:ec2launchtemplate:version", "999")
			case "market":
				instance.InstanceLifecycle = ""
			case "placement":
				instance.SubnetId = aws.String(snapshot.Receipt.Plan.Choices[1].SubnetID)
			case "root-delete":
				instance.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
			case "extra-worker":
				instance.InstanceId = aws.String("i-fffffffffffffffff")
				api.instances["i-fffffffffffffffff"] = instance
			}
			api.scanPages[0].out.Reservations[0].Instances = []types.Instance{instance}
			if mode == "duplicate" {
				api.scanPages[0].out.Reservations[0].Instances = append(api.scanPages[0].out.Reservations[0].Instances, instance)
			}
			out, err := ReconcileLaunch(context.Background(), api, snapshot)
			if err == nil || out.Bounded || len(out.Workers) == 0 || len(out.Workers[0].Volumes) == 0 {
				t.Fatalf("contradiction accepted or identity lost: %+v err=%v", out, err)
			}
			if mode == "extra-worker" && len(out.Workers) != 2 {
				t.Fatal("unexpected worker identity was discarded")
			}
		})
	}
}

func TestReconcileLaunchFleetEvidenceAndExpiry(t *testing.T) {
	for _, mode := range []string{"matching", "token", "scope", "type", "template", "target", "root", "reported-placement", "missing", "expired", "unavailable", "cycle", "unknown-fulfilled"} {
		t.Run(mode, func(t *testing.T) {
			state := "complete"
			if mode == "unknown-fulfilled" {
				state = "unknown"
			}
			snapshot, api := reconcileFixture(t, state, 1, 1)
			fleet := reconcileFleetFixture(t, snapshot)
			switch mode {
			case "token":
				fleet.ClientToken = aws.String(strings.Repeat("b", 64))
			case "scope":
				changeFleetTestTag(fleet.Tags, "Owner", "different")
			case "type":
				fleet.Type = types.FleetTypeMaintain
			case "template":
				fleet.LaunchTemplateConfigs[0].LaunchTemplateSpecification.Version = aws.String("999")
			case "target":
				fleet.TargetCapacitySpecification.TotalTargetCapacity = aws.Int32(3)
			case "root":
				fleet.LaunchTemplateConfigs[0].Overrides[0].BlockDeviceMappings[0].Ebs.Encrypted = aws.Bool(false)
			case "reported-placement":
				fleet.Instances[0].InstanceType = "m7i.2xlarge"
			}
			api.fleetPages = []reconcileFleetPage{{out: &ec2.DescribeFleetsOutput{Fleets: []types.FleetData{fleet}}}}
			switch mode {
			case "missing":
				api.fleetPages = nil
			case "expired":
				api.fleetPages = []reconcileFleetPage{{err: &smithy.GenericAPIError{Code: "InvalidFleetId.NotFound", Message: "expired"}}}
			case "unavailable":
				api.fleetPages = []reconcileFleetPage{{err: errors.New("secret AWS diagnostics")}}
			case "cycle":
				api.fleetPages[0].out.NextToken = aws.String("repeat")
				api.fleetPages = append(api.fleetPages, reconcileFleetPage{out: &ec2.DescribeFleetsOutput{NextToken: aws.String("repeat")}})
			}
			out, err := ReconcileLaunch(context.Background(), api, snapshot)
			valid := mode == "matching" || mode == "missing" || mode == "expired" || mode == "unknown-fulfilled"
			if (err == nil) != valid || out.Bounded != (valid && mode != "unknown-fulfilled") || len(out.Workers) != 1 {
				t.Fatalf("Fleet evidence mishandled: bounded=%v workers=%+v err=%v", out.Bounded, out.Workers, err)
			}
		})
	}
}

func TestReconcileLaunchHistoricalFulfillmentSurvivesTeardown(t *testing.T) {
	for _, mode := range []string{"terminated", "shutting-down", "absent", "not-found", "contradiction", "one-gone-one-live", "unknown-terminated"} {
		t.Run(mode, func(t *testing.T) {
			state, count := "complete", 1
			if mode == "unknown-terminated" {
				state = "unknown"
			}
			if mode == "one-gone-one-live" {
				count = 2
			}
			snapshot, api := reconcileFixture(t, state, count, count)
			id := snapshot.Receipt.Attempts[0].InstanceIDs[0]
			instance := api.instances[id]
			instance.State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
			if mode == "shutting-down" {
				instance.State.Name = types.InstanceStateNameShuttingDown
			}
			instance.IamInstanceProfile, instance.MetadataOptions, instance.SecurityGroups = nil, nil, nil
			instance.NetworkInterfaces, instance.BlockDeviceMappings, instance.PublicIpAddress = nil, nil, nil
			if mode == "contradiction" {
				instance.ImageId = aws.String("ami-ffffffff")
			}
			api.instances[id] = instance
			api.scanPages = []fleetInstancesPage{{out: &ec2.DescribeInstancesOutput{}}}
			if mode == "absent" || mode == "one-gone-one-live" {
				delete(api.instances, id)
			}
			if mode == "not-found" {
				api.exactErrors[id] = &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound", Message: "gone"}
			}
			out, err := ReconcileLaunch(context.Background(), api, snapshot)
			bounded := mode != "contradiction" && mode != "unknown-terminated"
			if out.Bounded != bounded || (err == nil) != bounded || len(out.Receipt.FulfilledIDs()) != count || len(out.Workers) != count {
				t.Fatalf("historical fulfillment mishandled: %+v err=%v", out, err)
			}
			if bounded && out.Workers[0].Status != "historical" {
				t.Fatalf("removed settings presented as currently verified: %+v", out.Workers[0])
			}
			if mode == "one-gone-one-live" && out.Workers[1].Status != "allocated" {
				t.Fatal("removed instance masked inspection of live worker")
			}
		})
	}
}

func TestReconcileLaunchExactPartialResponsesAndTerminalDuplicates(t *testing.T) {
	for _, mode := range []string{"partial-error", "terminal-duplicate", "terminal-cycle"} {
		t.Run(mode, func(t *testing.T) {
			snapshot, api := reconcileFixture(t, "complete", 1, 1)
			id := snapshot.Receipt.Attempts[0].InstanceIDs[0]
			instance := api.instances[id]
			api.scanPages = []fleetInstancesPage{{out: &ec2.DescribeInstancesOutput{}}}
			page := &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(api.account), Instances: []types.Instance{instance}}}}
			pages := []fleetInstancesPage{{out: page}}
			if mode == "partial-error" {
				extra := instance
				extra.InstanceId = aws.String("i-fffffffffffffffff")
				page.Reservations[0].Instances = append(page.Reservations[0].Instances, extra)
				pages[0].err = errors.New("private AWS diagnostic")
			} else {
				page.Reservations[0].Instances[0].State = &types.InstanceState{Name: types.InstanceStateNameTerminated}
				if mode == "terminal-duplicate" {
					page.Reservations[0].Instances = append(page.Reservations[0].Instances, page.Reservations[0].Instances[0])
				} else {
					page.NextToken = aws.String("repeat")
					pages = append(pages, fleetInstancesPage{out: &ec2.DescribeInstancesOutput{NextToken: aws.String("repeat")}})
				}
			}
			api.exactPages = map[string][]fleetInstancesPage{id: pages}
			out, err := ReconcileLaunch(context.Background(), api, snapshot)
			if out.Bounded || err == nil || len(out.Workers) == 0 || len(out.Workers[0].Volumes) != 1 {
				t.Fatalf("exact inspection hid incomplete evidence: bounded=%v workers=%+v err=%v", out.Bounded, out.Workers, err)
			}
			if mode == "partial-error" && (len(out.Workers) != 2 || len(out.Workers[1].Volumes) != 1) {
				t.Fatal("partial exact response discarded an unexpected worker or mapping")
			}
		})
	}
}

func TestReconcileLaunchRequiresOriginalSharedResponseLinkage(t *testing.T) {
	for _, mode := range []string{"missing-prepared", "claim-token", "missing-claim", "missing-response", "receipt-differs", "input-differs", "orphan-response"} {
		t.Run(mode, func(t *testing.T) {
			snapshot, api := reconcileFixture(t, "complete", 1, 1)
			id := snapshot.Receipt.Attempts[0].AttemptID
			switch mode {
			case "missing-prepared":
				delete(snapshot.Prepared, id)
			case "claim-token":
				claim := snapshot.Claims[id]
				claim.ClientToken = strings.Repeat("b", 64)
				snapshot.Claims[id] = claim
			case "missing-claim":
				delete(snapshot.Claims, id)
			case "missing-response":
				delete(snapshot.Responses, id)
			case "receipt-differs":
				snapshot.Receipt.Attempts[0].InstanceIDs = []string{}
			case "input-differs":
				response := snapshot.Responses[id]
				response.InputSHA256 = strings.Repeat("b", 64)
				snapshot.Responses[id] = response
			case "orphan-response":
				snapshot.Responses[strings.Repeat("b", 32)] = snapshot.Responses[id]
			}
			out, err := ReconcileLaunch(context.Background(), api, snapshot)
			if out.Bounded || err == nil || len(api.scanInputs) != 0 || len(api.exactInputs) != 0 || len(api.fleetInputs) != 0 || len(out.Workers) != 1 {
				t.Fatalf("invalid shared evidence reached AWS or lost known identity: %+v err=%v", out, err)
			}
		})
	}
}

func TestReconcileHistoricalCountSurvivesConflictingExtraWorker(t *testing.T) {
	for _, currentState := range []string{"gone", "contradictory"} {
		t.Run(currentState, func(t *testing.T) {
			snapshot, api := reconcileFixture(t, "complete", 1, 2)
			original := snapshot.Receipt.Attempts[0].InstanceIDs[0]
			if currentState == "gone" {
				delete(api.instances, original)
			} else {
				worker := api.instances[original]
				worker.ImageId = aws.String("ami-ffffffff")
				api.instances[original] = worker
			}
			extra := api.scanPages[0].out.Reservations[0].Instances[1]
			extra.ImageId = aws.String("ami-ffffffff")
			api.instances[aws.ToString(extra.InstanceId)] = extra
			api.scanPages[0].out.Reservations[0].Instances = []types.Instance{extra}
			observation, err := ReconcileLaunch(context.Background(), api, snapshot)
			if err == nil || observation.Bounded {
				t.Fatal("contradictory extra worker did not block retry")
			}
			out := (&RecoveryService{}).outcome(observation)
			if out.FulfilledCount != 1 || out.Attempts[0].FulfilledCount != 1 || out.MissingCount != nil || len(out.Workers) != 2 {
				t.Fatalf("historical fulfillment or unknown capacity lost: %+v", out)
			}
		})
	}
}
