package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

const normalizedFleetID = "fleet-01234567-89ab-cdef-0123-456789abcdef"

func normalizationFixture(t *testing.T) (LaunchPlan, AttemptReceipt) {
	t.Helper()
	r := receiptFixture(t)
	a := r.Attempts[0]
	a.State, a.FleetID, a.InstanceIDs, a.Errors = "dispatched", "", nil, nil
	return r.Plan, a
}

func fleetTestPool(plan LaunchPlan, n int) *types.LaunchTemplateAndOverridesResponse {
	choice := plan.Choices[n]
	return &types.LaunchTemplateAndOverridesResponse{
		LaunchTemplateSpecification: &types.FleetLaunchTemplateSpecification{LaunchTemplateId: aws.String(plan.Image.LaunchTemplateID), Version: aws.String(plan.Image.LaunchTemplateVersion)},
		Overrides:                   &types.FleetLaunchTemplateOverrides{InstanceType: types.InstanceType(choice.InstanceType), SubnetId: aws.String(choice.SubnetID), AvailabilityZone: aws.String(choice.AvailabilityZone), ImageId: aws.String(plan.Image.AMIID)},
	}
}

func fleetTestGroup(plan LaunchPlan, n int, ids ...string) types.CreateFleetInstance {
	choice := plan.Choices[n]
	return types.CreateFleetInstance{InstanceIds: ids, InstanceType: types.InstanceType(choice.InstanceType),
		SubnetId: aws.String(choice.SubnetID), AvailabilityZone: aws.String(choice.AvailabilityZone), Lifecycle: types.InstanceLifecycle(plan.Market),
		LaunchTemplateAndOverrides: fleetTestPool(plan, n)}
}

func fleetTestError(plan LaunchPlan, n int, code string) types.CreateFleetError {
	return types.CreateFleetError{ErrorCode: aws.String(code), ErrorMessage: aws.String("secret AWS request detail"), Lifecycle: types.InstanceLifecycle(plan.Market), LaunchTemplateAndOverrides: fleetTestPool(plan, n)}
}

func TestFleetCompletePartialAndZeroPreserveEveryDistinctIdentityAndError(t *testing.T) {
	for _, scenario := range []string{"full", "full-omitted-empty-errors", "partial", "zero", "full-with-pool-errors", "on-demand"} {
		t.Run(scenario, func(t *testing.T) {
			plan, attempt := normalizationFixture(t)
			if scenario == "on-demand" {
				plan.Market = "on-demand"
			}
			out := &ec2.CreateFleetOutput{FleetId: aws.String(normalizedFleetID), Instances: []types.CreateFleetInstance{}, Errors: []types.CreateFleetError{}}
			wantIDs := []string{"i-0123456789abcdef0", "i-0123456789abcdef1"}
			if scenario == "partial" {
				wantIDs = wantIDs[:1]
			} else if scenario == "zero" {
				wantIDs = []string{}
			}
			// Service response order is deliberately reversed; IDs, worker names and
			// remaining capacity do not depend on group or pool-error order.
			for n := len(wantIDs) - 1; n >= 0; n-- {
				out.Instances = append(out.Instances, fleetTestGroup(plan, n, wantIDs[n]))
			}
			wantErrors := 0
			if scenario == "partial" || scenario == "zero" || scenario == "full-with-pool-errors" {
				out.Errors = []types.CreateFleetError{fleetTestError(plan, 0, "InsufficientInstanceCapacity"), fleetTestError(plan, 0, "MaxSpotInstanceCountExceeded"), fleetTestError(plan, 2, "UnknownSecretCode")}
				wantErrors = 3
			} else if scenario == "full-omitted-empty-errors" {
				out.Errors = nil
			}
			result, workers := NormalizeFleetResponse(plan, attempt, out, nil)
			if result.State != "complete" || result.FleetID != normalizedFleetID || !reflect.DeepEqual(result.InstanceIDs, wantIDs) || len(workers) != len(wantIDs) || len(result.Errors) != wantErrors {
				t.Fatalf("lost normalized outcome: %+v workers=%+v", result, workers)
			}
			for n, worker := range workers {
				wantName, _ := WorkerName(plan.BaseName, wantIDs[n])
				if worker.ID != wantIDs[n] || worker.Name != wantName || worker.Status != "not_observed" || worker.State != "unknown" || worker.Readiness != "not_observed" || worker.TemplateID != plan.Image.LaunchTemplateID || worker.TemplateVersion != plan.Image.LaunchTemplateVersion || worker.Image != plan.Image.AMIID || worker.Market != plan.Market || worker.AttemptID != attempt.AttemptID || worker.Type != plan.Choices[n].InstanceType || worker.SubnetID != plan.Choices[n].SubnetID || worker.AvailabilityZone != plan.Choices[n].AvailabilityZone {
					t.Fatalf("worker metadata: %+v", worker)
				}
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "secret") || strings.Contains(string(data), "UnknownSecretCode") {
				t.Fatalf("raw AWS diagnostics exposed: %s", data)
			}
		})
	}
}

func TestFleetMalformedAndIncompleteOutcomesRemainUnknownWithoutLosingIDs(t *testing.T) {
	for _, scenario := range []string{"nil-output", "no-fleet", "invalid-fleet", "nil-instances", "empty-no-errors", "partial-no-errors", "duplicate", "cross-pool-duplicate", "malformed-id", "no-type", "wrong-pool", "wrong-market", "wrong-platform", "wrong-template", "wrong-image", "conflicting-placement", "weighted", "missing-error-code", "missing-error-pool", "wrong-error-market", "empty-group", "over-target", "response-and-error"} {
		t.Run(scenario, func(t *testing.T) {
			plan, attempt := normalizationFixture(t)
			out := &ec2.CreateFleetOutput{FleetId: aws.String(normalizedFleetID), Instances: []types.CreateFleetInstance{fleetTestGroup(plan, 0, "i-0123456789abcdef0", "i-0123456789abcdef1")}, Errors: []types.CreateFleetError{}}
			var callErr error
			wantCount := 2
			switch scenario {
			case "nil-output":
				out, wantCount = nil, 0
			case "no-fleet":
				out.FleetId = nil
			case "invalid-fleet":
				out.FleetId = aws.String("secret-invalid-fleet")
			case "nil-instances":
				out.Instances, wantCount = nil, 0
				out.Errors = []types.CreateFleetError{fleetTestError(plan, 0, "InsufficientInstanceCapacity")}
			case "empty-no-errors":
				out.Instances, wantCount = []types.CreateFleetInstance{}, 0
			case "partial-no-errors":
				out.Instances[0].InstanceIds, wantCount = []string{"i-0123456789abcdef0"}, 1
			case "duplicate":
				out.Instances[0].InstanceIds = append(out.Instances[0].InstanceIds, "i-0123456789abcdef0")
			case "cross-pool-duplicate":
				out.Instances = append(out.Instances, fleetTestGroup(plan, 1, "i-0123456789abcdef0"))
			case "malformed-id":
				out.Instances[0].InstanceIds = append(out.Instances[0].InstanceIds, "secret-invalid-instance")
			case "no-type":
				out.Instances[0].InstanceType = ""
				out.Instances[0].LaunchTemplateAndOverrides = nil
			case "wrong-pool":
				out.Instances[0].SubnetId = aws.String("subnet-aaaaaaaa")
			case "wrong-market":
				out.Instances[0].Lifecycle = "on-demand"
			case "wrong-platform":
				out.Instances[0].Platform = "windows"
			case "wrong-template":
				out.Instances[0].LaunchTemplateAndOverrides.LaunchTemplateSpecification.Version = aws.String("$Latest")
			case "wrong-image":
				out.Instances[0].LaunchTemplateAndOverrides.Overrides.ImageId = aws.String("ami-aaaaaaaa")
			case "conflicting-placement":
				out.Instances[0].LaunchTemplateAndOverrides.Overrides.AvailabilityZone = aws.String("us-east-2a")
			case "weighted":
				out.Instances[0].LaunchTemplateAndOverrides.Overrides.WeightedCapacity = aws.Float64(2)
			case "missing-error-code", "missing-error-pool", "wrong-error-market":
				out.Errors = []types.CreateFleetError{fleetTestError(plan, 0, "InsufficientInstanceCapacity")}
				if scenario == "missing-error-code" {
					out.Errors[0].ErrorCode = nil
				} else if scenario == "missing-error-pool" {
					out.Errors[0].LaunchTemplateAndOverrides = nil
				} else {
					out.Errors[0].Lifecycle = "on-demand"
				}
			case "empty-group":
				out.Instances = append(out.Instances, fleetTestGroup(plan, 1))
			case "over-target":
				out.Instances[0].InstanceIds = append(out.Instances[0].InstanceIds, "i-0123456789abcdef2")
				wantCount = 3
			case "response-and-error":
				callErr = &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "secret API detail"}
			}
			result, workers := NormalizeFleetResponse(plan, attempt, out, callErr)
			if result.State != "unknown" || len(result.InstanceIDs) != wantCount || len(workers) != wantCount || len(result.Errors) == 0 {
				t.Fatalf("unsafe or incomplete normalization: %+v workers=%+v", result, workers)
			}
			for n := 0; n < wantCount; n++ {
				if result.InstanceIDs[n] != fmt.Sprintf("i-0123456789abcdef%d", n) {
					t.Fatalf("known ID lost: %+v", result)
				}
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "secret") {
				t.Fatalf("raw malformed data exposed: %s", data)
			}
		})
	}
}

func TestFleetErrorsDistinguishStandaloneRejectionFromUncertainAllocation(t *testing.T) {
	for _, code := range []string{"UnauthorizedOperation", "AuthFailure", "InsufficientInstanceCapacity", "MaxSpotInstanceCountExceeded", "VcpuLimitExceeded", "InvalidParameterValue", "PendingVerification", "RequestLimitExceeded", "ThrottlingException", "IdempotentParameterMismatch", "InternalError", "secret-code", "transport", "cancelled"} {
		t.Run(code, func(t *testing.T) {
			plan, attempt := normalizationFixture(t)
			var callErr error = fmt.Errorf("wrapped: %w", &smithy.GenericAPIError{Code: code, Message: "secret response"})
			wantState := "rejected"
			switch code {
			case "RequestLimitExceeded", "ThrottlingException", "IdempotentParameterMismatch", "InternalError", "secret-code":
				wantState = "unknown"
			case "transport":
				callErr, wantState = errors.New("secret transport detail"), "unknown"
			case "cancelled":
				callErr, wantState = context.Canceled, "unknown"
			}
			result, workers := NormalizeFleetResponse(plan, attempt, nil, callErr)
			if result.State != wantState || len(result.InstanceIDs) != 0 || len(workers) != 0 || len(result.Errors) != 1 {
				t.Fatalf("error classification: %+v workers=%+v", result, workers)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "secret") {
				t.Fatalf("raw AWS error exposed: %s", data)
			}
			attempt.FleetID, attempt.InstanceIDs = normalizedFleetID, []string{"i-0123456789abcdef0"}
			result, workers = NormalizeFleetResponse(plan, attempt, nil, callErr)
			if result.State != "unknown" || result.FleetID != normalizedFleetID || len(result.InstanceIDs) != 1 || len(workers) != 1 {
				t.Fatalf("earlier known allocation lost: %+v", result)
			}
		})
	}
}

func TestFleetRetainsConflictingKnownFleetIdentities(t *testing.T) {
	plan, attempt := normalizationFixture(t)
	attempt.FleetID = "fleet-11234567-89ab-cdef-0123-456789abcdef"
	out := &ec2.CreateFleetOutput{FleetId: aws.String(normalizedFleetID), Instances: []types.CreateFleetInstance{fleetTestGroup(plan, 0, "i-0123456789abcdef0", "i-0123456789abcdef1")}}
	result, _ := NormalizeFleetResponse(plan, attempt, out, nil)
	if result.State != "unknown" || result.FleetID != attempt.FleetID || len(result.Errors) != 2 || result.Errors[0].ResourceID != normalizedFleetID || len(result.InstanceIDs) != 2 {
		t.Fatalf("Fleet conflict evidence lost: %+v", result)
	}
}

func TestFleetResponsePlacementCanUseReportedOverrides(t *testing.T) {
	plan, attempt := normalizationFixture(t)
	group := fleetTestGroup(plan, 0, "i-0123456789abcdef0", "i-0123456789abcdef1")
	group.InstanceType, group.SubnetId, group.AvailabilityZone = "", nil, nil
	// The response may omit the AZ when the exact subnet is present. Its
	// foundation-approved mapping still identifies the sole possible AZ.
	group.LaunchTemplateAndOverrides.Overrides.AvailabilityZone = nil
	result, workers := NormalizeFleetResponse(plan, attempt, &ec2.CreateFleetOutput{FleetId: aws.String(normalizedFleetID), Instances: []types.CreateFleetInstance{group}}, nil)
	if result.State != "complete" || len(workers) != 2 || workers[0].SubnetID != plan.Choices[0].SubnetID || workers[0].AvailabilityZone != plan.Choices[0].AvailabilityZone {
		t.Fatalf("reported placement lost: %+v workers=%+v", result, workers)
	}
}

func TestFleetBuilderRejectsChangedIdentityPinsAndEffectiveSettings(t *testing.T) {
	for _, scenario := range []string{"token", "attempt", "count-zero", "count-excessive", "market", "floating-version", "zero-version", "malformed-image", "wrong-template-prefix", "disk-small", "disk-unencrypted", "disk-retained", "disk-type", "missing-tag", "foreign-tag", "extra-tag", "duplicate-pool", "wrong-subnet-prefix", "multiple-subnets-in-zone", "on-demand-multiple-types"} {
		t.Run(scenario, func(t *testing.T) {
			plan, attempt := normalizationFixture(t)
			switch scenario {
			case "token":
				attempt.ClientToken = strings.Repeat("b", 64)
			case "attempt":
				attempt.AttemptID = strings.Repeat("b", 32)
			case "count-zero":
				attempt.RequestedCount = 0
			case "count-excessive":
				attempt.RequestedCount = 3
			case "market":
				plan.Market = "maintain"
			case "floating-version":
				plan.Image.LaunchTemplateVersion = "$Latest"
			case "zero-version":
				plan.Image.LaunchTemplateVersion = "0"
			case "malformed-image":
				plan.Image.AMIID = "resolve:ssm:/some/image"
			case "wrong-template-prefix":
				plan.Image.LaunchTemplateID = "ami-12345678"
			case "disk-small":
				plan.RootDisk.SizeGB = 7
			case "disk-unencrypted":
				plan.RootDisk.Encrypted = false
			case "disk-retained":
				plan.RootDisk.DeleteOnTermination = false
			case "disk-type":
				plan.RootDisk.Type = "gp2"
			case "missing-tag":
				delete(plan.CreationTags, "RequestId")
			case "foreign-tag":
				plan.CreationTags["Owner"] = "other"
			case "extra-tag":
				plan.CreationTags["Unapproved"] = "value"
			case "duplicate-pool":
				plan.Choices = append(plan.Choices, plan.Choices[0])
			case "wrong-subnet-prefix":
				plan.Choices[0].SubnetID = "ami-12345678"
			case "multiple-subnets-in-zone":
				plan.Choices[0].AvailabilityZone = plan.Choices[1].AvailabilityZone
			case "on-demand-multiple-types":
				plan.Market = "on-demand"
			}
			if scenario != "token" {
				attempt.ClientToken = attemptToken(plan.Digest(), attempt.AttemptID, attempt.RequestedCount)
			}
			if input, err := BuildFleetInput(plan, attempt); err == nil || input != nil {
				t.Fatalf("invalid Fleet input accepted: %+v %v", input, err)
			}
		})
	}
}
