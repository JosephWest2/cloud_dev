package lifecycle

import (
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

var fleetIDRE = regexp.MustCompile(`^fleet-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var fleetResourceIDRE = regexp.MustCompile(`^(ami|lt|subnet)-([0-9a-f]{8}|[0-9a-f]{17})$`)
var fleetTypeRE = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9-]+$`)

// BuildFleetInput is a pure mapping of immutable, previously verified pins. The
// caller must verify the deployed foundation and win the permanent dispatch
// claim before sending this input. There is no capacity maintenance or fallback.
func BuildFleetInput(plan LaunchPlan, attempt AttemptReceipt) (*ec2.CreateFleetInput, error) {
	invalid := failure("launch_invalid", "Fleet requires a pinned plan and matching immutable attempt")
	version, err := strconv.ParseUint(plan.Image.LaunchTemplateVersion, 10, 64)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != plan.Image.LaunchTemplateVersion || !validFleetResourceID(plan.Image.LaunchTemplateID, "lt") || !validFleetResourceID(plan.Image.AMIID, "ami") || plan.Image.RootDeviceName != "/dev/sda1" || len(plan.Choices) == 0 {
		return nil, invalid
	}
	if plan.SchemaVersion != 1 || !ValidRequest(plan.RequestID) || !ValidRequest(attempt.AttemptID) || attempt.RequestedCount < 1 || attempt.RequestedCount > plan.RequestedCount || plan.RequestedCount > config.HardMaxCount || attempt.ClientToken != attemptToken(plan.Digest(), attempt.AttemptID, attempt.RequestedCount) {
		return nil, invalid
	}
	wantID, err := AttemptID(plan.RequestID, attempt.ParentID)
	if err != nil || wantID != attempt.AttemptID || plan.RootDisk.SizeGB < 8 || plan.RootDisk.SizeGB > 16384 || plan.RootDisk.SizeGB < plan.Image.MinimumRootDiskGB || plan.RootDisk.Type != "gp3" || !plan.RootDisk.Encrypted || !plan.RootDisk.DeleteOnTermination {
		return nil, invalid
	}
	tags, err := plan.AttemptTags(attempt.AttemptID)
	if err != nil {
		return nil, err
	}
	wantTags := map[string]string{"ManagedBy": "devbox", "Deployment": plan.Deployment, "Owner": plan.Owner, "Profile": plan.Profile,
		"Name": plan.BaseName, "BaseName": plan.BaseName, "RequestId": plan.RequestID, "BatchId": plan.RequestID,
		"CreatedAt": plan.CreatedAt, "NamingVersion": "1", "AttemptId": attempt.AttemptID}
	if plan.Group != "" {
		wantTags["Group"] = plan.Group
	}
	_, planDateErr := time.Parse(time.RFC3339Nano, plan.CreatedAt)
	_, attemptDateErr := time.Parse(time.RFC3339Nano, attempt.CreatedAt)
	if !reflect.DeepEqual(tags, wantTags) || !ValidName(plan.BaseName) || plan.Profile != "agent" || !nameRE.MatchString(plan.Deployment) || !nameRE.MatchString(plan.Owner) || (plan.Group != "" && !ValidGroup(plan.Group)) || planDateErr != nil || attemptDateErr != nil {
		return nil, invalid
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	in := &ec2.CreateFleetInput{
		Type: types.FleetTypeInstant, ClientToken: aws.String(attempt.ClientToken),
		TargetCapacitySpecification: &types.TargetCapacitySpecificationRequest{
			TotalTargetCapacity: aws.Int32(int32(attempt.RequestedCount)),
			SpotTargetCapacity:  aws.Int32(0), OnDemandTargetCapacity: aws.Int32(0),
		},
	}
	switch plan.Market {
	case "spot":
		in.TargetCapacitySpecification.DefaultTargetCapacityType = types.DefaultTargetCapacityTypeSpot
		in.TargetCapacitySpecification.SpotTargetCapacity = aws.Int32(int32(attempt.RequestedCount))
		in.SpotOptions = &types.SpotOptionsRequest{AllocationStrategy: types.SpotAllocationStrategyPriceCapacityOptimized}
	case "on-demand":
		in.TargetCapacitySpecification.DefaultTargetCapacityType = types.DefaultTargetCapacityTypeOnDemand
		in.TargetCapacitySpecification.OnDemandTargetCapacity = aws.Int32(int32(attempt.RequestedCount))
		in.OnDemandOptions = &types.OnDemandOptionsRequest{AllocationStrategy: types.FleetOnDemandAllocationStrategyLowestPrice}
	default:
		return nil, invalid
	}
	choices := append([]config.LaunchChoice(nil), plan.Choices...)
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].InstanceType != choices[j].InstanceType {
			return choices[i].InstanceType < choices[j].InstanceType
		}
		return choices[i].SubnetID < choices[j].SubnetID
	})
	launch := types.FleetLaunchTemplateConfigRequest{LaunchTemplateSpecification: &types.FleetLaunchTemplateSpecificationRequest{
		LaunchTemplateId: aws.String(plan.Image.LaunchTemplateID), Version: aws.String(plan.Image.LaunchTemplateVersion),
	}}
	seen := map[config.LaunchChoice]bool{}
	zones := map[string]string{}
	for _, choice := range choices {
		if !fleetTypeRE.MatchString(choice.InstanceType) || !validFleetResourceID(choice.SubnetID, "subnet") || (choice.AvailabilityZone != "us-east-2a" && choice.AvailabilityZone != "us-east-2b" && choice.AvailabilityZone != "us-east-2c") || seen[choice] || (zones[choice.AvailabilityZone] != "" && zones[choice.AvailabilityZone] != choice.SubnetID) || (plan.Market == "on-demand" && choice.InstanceType != choices[0].InstanceType) {
			return nil, invalid
		}
		seen[choice], zones[choice.AvailabilityZone] = true, choice.SubnetID
		launch.Overrides = append(launch.Overrides, types.FleetLaunchTemplateOverridesRequest{
			InstanceType: types.InstanceType(choice.InstanceType), SubnetId: aws.String(choice.SubnetID), AvailabilityZone: aws.String(choice.AvailabilityZone),
			ImageId: aws.String(plan.Image.AMIID), MetadataOptions: &types.FleetInstanceMetadataOptionsRequest{HttpTokens: types.FleetHttpTokensStateRequired},
			BlockDeviceMappings: []types.FleetBlockDeviceMappingRequest{{DeviceName: aws.String(plan.Image.RootDeviceName), Ebs: &types.FleetEbsBlockDeviceRequest{
				VolumeSize: aws.Int32(int32(plan.RootDisk.SizeGB)), VolumeType: types.VolumeTypeGp3, Encrypted: aws.Bool(true), DeleteOnTermination: aws.Bool(true),
			}}},
		})
	}
	in.LaunchTemplateConfigs = []types.FleetLaunchTemplateConfigRequest{launch}
	for _, resourceType := range []types.ResourceType{types.ResourceTypeFleet, types.ResourceTypeInstance, types.ResourceTypeVolume} {
		spec := types.TagSpecification{ResourceType: resourceType}
		for _, key := range keys {
			spec.Tags = append(spec.Tags, types.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
		}
		in.TagSpecifications = append(in.TagSpecifications, spec)
	}
	return in, nil
}

func validFleetResourceID(value, prefix string) bool {
	return strings.HasPrefix(value, prefix+"-") && fleetResourceIDRE.MatchString(value)
}

// NormalizeFleetResponse preserves valid known identities even in contradictory
// or incomplete envelopes. A complete synchronous response bounds allocation;
// pool-error cardinality never represents missing workers. Worker pins here are
// reported or intended pins, pending the caller's exact Describe verification.
//
// A standalone definitive rejection can establish zero allocation. A caller
// whose SDK retried an earlier ambiguous send must downgrade such a rejection
// to unknown: the earlier send may have allocated before its response was lost.
func NormalizeFleetResponse(plan LaunchPlan, attempt AttemptReceipt, out *ec2.CreateFleetOutput, callErr error) (AttemptReceipt, []WorkerOutcome) {
	result := attempt
	result.State = "unknown"
	result.InstanceIDs = append([]string{}, attempt.InstanceIDs...)
	result.Errors = append([]ResourceError{}, attempt.Errors...)
	workers := map[string]WorkerOutcome{}
	for _, id := range attempt.InstanceIDs {
		if instanceRE.MatchString(id) {
			workers[id] = fleetWorker(plan, attempt, id, config.LaunchChoice{})
		}
	}
	valid := out != nil && callErr == nil
	responseIDs := map[string]bool{}
	if out != nil {
		fleetID := aws.ToString(out.FleetId)
		if !fleetIDRE.MatchString(fleetID) {
			valid = false
		} else if result.FleetID != "" && result.FleetID != fleetID {
			valid = false
			result.Errors = append(result.Errors, ResourceError{ResourceID: fleetID, Code: "fleet_response_conflict", Message: "The response reports a different Fleet identity; preserve both identities and resume without allocating."})
		} else {
			result.FleetID = fleetID
		}
		if out.Instances == nil {
			valid = false
		}
		for _, group := range out.Instances {
			choice, poolOK := fleetResponsePool(plan, string(group.InstanceType), aws.ToString(group.SubnetId), aws.ToString(group.AvailabilityZone), group.LaunchTemplateAndOverrides)
			if !poolOK || string(group.Lifecycle) != plan.Market || group.Platform != "" || len(group.InstanceIds) == 0 {
				valid = false
			}
			for _, id := range group.InstanceIds {
				if !instanceRE.MatchString(id) {
					valid = false
					continue
				}
				if responseIDs[id] {
					valid = false
					continue
				}
				responseIDs[id] = true
				workers[id] = fleetWorker(plan, attempt, id, choice)
			}
		}
		for _, poolError := range out.Errors {
			choice, poolOK := fleetResponsePool(plan, "", "", "", poolError.LaunchTemplateAndOverrides)
			if !poolOK || string(poolError.Lifecycle) != plan.Market || aws.ToString(poolError.ErrorCode) == "" {
				valid = false
			}
			diagnostic, _ := fleetDiagnostic(aws.ToString(poolError.ErrorCode))
			if diagnostic.Code == "launch_outcome_unknown" {
				diagnostic = ResourceError{Code: "fleet_pool_error", Message: "EC2 reported an unrecognized pool error; retain successful workers and inspect the verified allocation outcome before retrying."}
			}
			diagnostic.InstanceType, diagnostic.SubnetID = choice.InstanceType, choice.SubnetID
			result.Errors = append(result.Errors, diagnostic)
		}
		// Full distinct fulfillment has no missing count to infer. Otherwise an
		// explicit instance set and capacity-error set must both be present.
		if len(responseIDs) < attempt.RequestedCount && len(out.Errors) == 0 {
			valid = false
		}
		if len(responseIDs) > attempt.RequestedCount {
			valid = false
		}
		for _, id := range attempt.InstanceIDs {
			if !responseIDs[id] {
				valid = false
			}
		}
	}
	result.InstanceIDs = make([]string, 0, len(workers))
	for id := range workers {
		result.InstanceIDs = append(result.InstanceIDs, id)
	}
	sort.Strings(result.InstanceIDs)
	if valid {
		result.State = "complete"
	} else if callErr != nil {
		var apiErr smithy.APIError
		code := ""
		if errors.As(callErr, &apiErr) {
			code = apiErr.ErrorCode()
		}
		diagnostic, rejected := fleetDiagnostic(code)
		result.Errors = append(result.Errors, diagnostic)
		if rejected && out == nil && result.FleetID == "" && len(result.InstanceIDs) == 0 {
			result.State = "rejected"
		}
	} else {
		result.Errors = append(result.Errors, ResourceError{Code: "fleet_response_incomplete", Message: "The Fleet response does not bound allocation; preserve all known identities and resume without allocating."})
	}
	ordered := make([]WorkerOutcome, 0, len(workers))
	for _, id := range result.InstanceIDs {
		ordered = append(ordered, workers[id])
	}
	return result, ordered
}

// Top-level instance placement and nested overrides may both be present. Fill
// omitted duplicate fields, but reject conflicting values and unapproved pools.
func fleetResponsePool(plan LaunchPlan, instanceType, subnet, zone string, launch *types.LaunchTemplateAndOverridesResponse) (config.LaunchChoice, bool) {
	valid := true
	if launch != nil {
		if spec := launch.LaunchTemplateSpecification; spec != nil {
			valid = aws.ToString(spec.LaunchTemplateId) == plan.Image.LaunchTemplateID && aws.ToString(spec.Version) == plan.Image.LaunchTemplateVersion
		}
		if override := launch.Overrides; override != nil {
			merge := func(current *string, reported string) {
				if *current != "" && reported != "" && *current != reported {
					valid = false
				}
				if *current == "" {
					*current = reported
				}
			}
			merge(&instanceType, string(override.InstanceType))
			merge(&subnet, aws.ToString(override.SubnetId))
			merge(&zone, aws.ToString(override.AvailabilityZone))
			if (override.ImageId != nil && aws.ToString(override.ImageId) != plan.Image.AMIID) || override.InstanceRequirements != nil || (override.WeightedCapacity != nil && aws.ToFloat64(override.WeightedCapacity) != 1) {
				valid = false
			}
		}
	}
	for _, choice := range plan.Choices {
		if choice.InstanceType == instanceType && choice.SubnetID == subnet && (zone == "" || choice.AvailabilityZone == zone) {
			return choice, valid
		}
	}
	// Never display arbitrary strings supplied by a malformed SDK response.
	return config.LaunchChoice{}, false
}

func fleetWorker(plan LaunchPlan, attempt AttemptReceipt, id string, choice config.LaunchChoice) WorkerOutcome {
	name, _ := WorkerName(plan.BaseName, id)
	return WorkerOutcome{
		Instance: Instance{ID: id, Name: name, RequestID: plan.RequestID, Profile: plan.Profile, CreatedAt: plan.CreatedAt,
			Image: plan.Image.AMIID, Type: choice.InstanceType, Market: plan.Market, TemplateID: plan.Image.LaunchTemplateID, TemplateVersion: plan.Image.LaunchTemplateVersion,
			State: "unknown", SSM: "not_observed", Bootstrap: "not_observed", Readiness: "not_observed", RootDeletion: "unavailable", Volumes: []Volume{}},
		Group: plan.Group, BaseName: plan.BaseName, AttemptID: attempt.AttemptID, SubnetID: choice.SubnetID, AvailabilityZone: choice.AvailabilityZone, Status: "not_observed",
	}
}

// Raw AWS messages and unknown error-code strings can contain account data or
// request content. Store only stable allowlisted diagnostics, once per error.
func fleetDiagnostic(code string) (ResourceError, bool) {
	switch code {
	case "UnauthorizedOperation", "AuthFailure", "AccessDenied", "AccessDeniedException":
		return ResourceError{Code: "launch_permission_denied", Message: "EC2 denied allocation; check the restricted operator policy, launch tags and pinned dependencies."}, true
	case "InsufficientInstanceCapacity", "InsufficientHostCapacity", "UnfulfillableCapacity":
		return ResourceError{Code: "capacity_unavailable", Message: "EC2 could not supply the selected capacity; retain successful workers and retry only proven missing capacity."}, true
	case "MaxSpotInstanceCountExceeded", "SpotInstanceCountLimitExceeded", "InstanceLimitExceeded", "VcpuLimitExceeded", "MaxFleetCountExceeded":
		return ResourceError{Code: "launch_quota_exceeded", Message: "EC2 reported a capacity quota limit; check the account's EC2 quotas before retrying proven missing capacity."}, true
	case "InvalidParameter", "InvalidParameterValue", "InvalidParameterCombination", "MissingParameter", "Unsupported", "UnsupportedOperation":
		return ResourceError{Code: "launch_parameters_rejected", Message: "EC2 rejected the pinned launch parameters; verify and re-export the foundation before authorizing another request."}, true
	case "PendingVerification":
		return ResourceError{Code: "launch_pending_verification", Message: "EC2 requires account verification; wait for AWS confirmation and resume this request before requesting more capacity."}, true
	case "RequestLimitExceeded", "Throttling", "ThrottlingException", "RequestThrottled":
		return ResourceError{Code: "launch_throttled", Message: "EC2 throttled the request; preserve this request identity and resume to reconcile allocation."}, false
	case "IdempotentParameterMismatch":
		return ResourceError{Code: "launch_identity_conflict", Message: "EC2 reported conflicting parameters for this identity; reconcile the original request without allocating."}, false
	default:
		return ResourceError{Code: "launch_outcome_unknown", Message: "EC2 did not provide a recognized allocation outcome; preserve all known identities and resume without allocating."}, false
	}
}
