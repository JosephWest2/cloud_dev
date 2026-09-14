package lifecycle

import (
	"context"
	"net/netip"
	"sort"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type FleetInventory interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
}

type fleetWorkerObservation struct {
	worker                                WorkerOutcome
	seen, mismatch, pending, rootVerified bool
	rootID                                string
}

// VerifyFleetWorkers observes exact known IDs without changing allocation
// completeness. Missing resources, transient reads and incomplete startup never
// prove zero fulfillment. Every known identity and volume mapping survives an
// observation failure, including mappings learned from partial API responses.
// This verifies current live settings. Teardown can remove those observations;
// the immutable original Fleet response must still count historical fulfillment.
func VerifyFleetWorkers(ctx context.Context, api FleetInventory, plan LaunchPlan, attempt AttemptReceipt, known []WorkerOutcome) ([]WorkerOutcome, error) {
	workers := map[string]*fleetWorkerObservation{}
	for _, worker := range known {
		worker.Volumes = append([]Volume{}, worker.Volumes...)
		if prior := workers[worker.ID]; prior != nil {
			prior.worker.Volumes = mergeFleetVolumes(prior.worker.Volumes, worker.Volumes)
			continue
		}
		worker.Status, worker.ObservationCode = "not_observed", "worker_not_observed"
		workers[worker.ID] = &fleetWorkerObservation{worker: worker}
	}
	for _, id := range attempt.InstanceIDs {
		if workers[id] == nil {
			workers[id] = &fleetWorkerObservation{worker: fleetWorker(plan, attempt, id, config.LaunchChoice{})}
		}
	}
	ids := make([]string, 0, len(workers))
	invalid := false
	for id, worker := range workers {
		if instanceRE.MatchString(id) {
			ids = append(ids, id)
		} else {
			worker.mismatch, invalid = true, true
		}
	}
	sort.Strings(ids)
	unavailable := func() error {
		return failure("worker_observation_unavailable", "Cannot verify every known worker yet; preserve all identities and resume inspection without allocating again.")
	}
	var readErr error
	if len(ids) > 0 && api == nil {
		readErr = unavailable()
	}
	if len(ids) > 0 && api != nil {
		input := &ec2.DescribeInstancesInput{InstanceIds: ids}
		tokens := map[string]bool{}
		for {
			if err := ctx.Err(); err != nil {
				readErr = err
				break
			}
			out, err := api.DescribeInstances(ctx, input)
			if out != nil {
				for _, reservation := range out.Reservations {
					for _, instance := range reservation.Instances {
						worker := workers[aws.ToString(instance.InstanceId)]
						if worker == nil || !instanceRE.MatchString(aws.ToString(instance.InstanceId)) {
							invalid = true
							continue
						}
						if worker.seen {
							worker.mismatch, invalid = true, true
						}
						worker.seen = true
						observeFleetInstance(worker, plan, attempt, aws.ToString(reservation.OwnerId), instance)
					}
				}
			}
			if err != nil || out == nil {
				readErr = unavailable()
				break
			}
			next := aws.ToString(out.NextToken)
			if next == "" {
				break
			}
			if tokens[next] {
				invalid = true
				break
			}
			tokens[next] = true
			input.NextToken = aws.String(next)
		}
	}
	// Retained historical mappings are evidence for cleanup. Only this read's
	// current root mapping can establish that the current instance is verified.
	roots := map[string][]*fleetWorkerObservation{}
	for _, worker := range workers {
		if worker.rootID != "" {
			roots[worker.rootID] = append(roots[worker.rootID], worker)
		}
	}
	volumeIDs := make([]string, 0, len(roots))
	for id, owners := range roots {
		volumeIDs = append(volumeIDs, id)
		if len(owners) != 1 {
			for _, worker := range owners {
				worker.mismatch = true
			}
		}
	}
	sort.Strings(volumeIDs)
	if len(volumeIDs) > 0 && api != nil {
		input := &ec2.DescribeVolumesInput{VolumeIds: volumeIDs}
		tokens, seen := map[string]bool{}, map[string]bool{}
		for {
			if err := ctx.Err(); err != nil {
				readErr = err
				break
			}
			out, err := api.DescribeVolumes(ctx, input)
			if out != nil {
				for _, volume := range out.Volumes {
					id := aws.ToString(volume.VolumeId)
					owners := roots[id]
					if len(owners) == 0 {
						invalid = true
						continue
					}
					for _, worker := range owners {
						if seen[id] {
							worker.mismatch, invalid = true, true
						}
						observeFleetVolume(worker, plan, attempt, volume)
					}
					seen[id] = true
				}
			}
			if err != nil || out == nil {
				readErr = unavailable()
				break
			}
			next := aws.ToString(out.NextToken)
			if next == "" {
				break
			}
			if tokens[next] {
				invalid = true
				break
			}
			tokens[next] = true
			input.NextToken = aws.String(next)
		}
	}
	result := make([]WorkerOutcome, 0, len(workers))
	mismatch, missing := false, false
	for _, worker := range workers {
		switch {
		case worker.mismatch:
			worker.worker.Status, worker.worker.ObservationCode = "identity_mismatch", "launch_identity_mismatch"
			mismatch = true
		case worker.seen && !worker.pending && worker.rootVerified:
			worker.worker.Status, worker.worker.ObservationCode = "allocated", ""
		default:
			worker.worker.Status, worker.worker.ObservationCode = "not_observed", "worker_not_observed"
			missing = true
		}
		result = append(result, worker.worker)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if mismatch {
		return result, failure("launch_identity_mismatch", "A known worker or root volume differs from the immutable launch pins; preserve every returned ID and inspect before further allocation.")
	}
	if invalid {
		return result, failure("worker_inventory_invalid", "EC2 inspection returned unexpected identities or repeated pagination; preserve known workers and retry inspection.")
	}
	if readErr != nil {
		return result, readErr
	}
	if missing {
		return result, unavailable()
	}
	return result, nil
}

func observeFleetInstance(observation *fleetWorkerObservation, plan LaunchPlan, attempt AttemptReceipt, owner string, instance types.Instance) {
	w := &observation.worker
	actual := record(instance)
	// Keep stable intended pins until the observation passes; the status reports
	// discrepancies. Newly learned mappings remain available even on mismatch.
	for _, old := range w.Volumes {
		if old.Root {
			for _, current := range actual.Volumes {
				if current.Root && old.ID != current.ID {
					observation.mismatch = true
				}
			}
		}
	}
	w.Volumes = mergeFleetVolumes(w.Volumes, actual.Volumes)
	w.RootDeletion = actual.RootDeletion
	switch actual.State {
	case "pending", "running", "stopping", "stopped", "shutting-down", "terminated":
		w.State = actual.State
	default:
		w.State = "unknown"
		observation.pending = true
	}
	tags, tagOK := fleetObservedTags(instance.Tags, plan, attempt)
	choice := config.LaunchChoice{InstanceType: string(instance.InstanceType), SubnetID: aws.ToString(instance.SubnetId)}
	if instance.Placement != nil {
		choice.AvailabilityZone = aws.ToString(instance.Placement.AvailabilityZone)
	}
	approved := false
	for _, candidate := range plan.Choices {
		approved = approved || candidate == choice
	}
	valid := owner == plan.Account && tagOK && approved && actual.Image == plan.Image.AMIID && actual.Market == plan.Market && tags["aws:ec2launchtemplate:id"] == plan.Image.LaunchTemplateID && tags["aws:ec2launchtemplate:version"] == plan.Image.LaunchTemplateVersion
	// A reported placement in the immutable Fleet response binds this worker,
	// even when another placement would also have been allowed by the plan.
	valid = valid && (w.Type == "" || w.Type == choice.InstanceType) && (w.SubnetID == "" || w.SubnetID == choice.SubnetID) && (w.AvailabilityZone == "" || w.AvailabilityZone == choice.AvailabilityZone)
	terminal := actual.State == "terminated" || actual.State == "shutting-down"
	if terminal && instance.IamInstanceProfile == nil {
		observation.pending = true
	} else {
		valid = valid && instance.IamInstanceProfile != nil && aws.ToString(instance.IamInstanceProfile.Arn) == plan.InstanceProfileARN
	}
	if terminal && len(instance.SecurityGroups) == 0 {
		observation.pending = true
	} else {
		valid = valid && exactFleetGroup(instance.SecurityGroups, plan.SecurityGroupID)
	}
	metadata := instance.MetadataOptions
	if terminal && metadata == nil {
		observation.pending = true
	} else {
		valid = valid && metadata != nil && metadata.HttpTokens == types.HttpTokensStateRequired && metadata.HttpEndpoint == types.InstanceMetadataEndpointStateEnabled && aws.ToInt32(metadata.HttpPutResponseHopLimit) == 1 && metadata.InstanceMetadataTags == types.InstanceMetadataTagsStateDisabled
	}
	if metadata != nil && metadata.State != types.InstanceMetadataOptionsStateApplied {
		observation.pending = true
	}
	// EC2 can publish a pending instance before its root attachment appears.
	// Missing root fields in that same incomplete observation are not proof of
	// contradictory settings. Present wrong values still fail closed, and no
	// root can be verified until its exact mapping and volume are observable.
	rootPending := actual.State == "pending" && len(instance.BlockDeviceMappings) == 0
	rootName := aws.ToString(instance.RootDeviceName)
	valid = valid && (rootName == plan.Image.RootDeviceName || rootPending && rootName == "")
	valid = valid && (instance.RootDeviceType == types.DeviceTypeEbs || rootPending && instance.RootDeviceType == "")
	if (terminal || rootPending) && len(instance.BlockDeviceMappings) == 0 {
		observation.pending = true
	} else {
		valid = valid && len(instance.BlockDeviceMappings) == 1
		if len(actual.Volumes) != 1 || !actual.Volumes[0].Root || !actual.Volumes[0].DeleteOnTermination {
			valid = false
		}
	}
	for _, mapping := range actual.Volumes {
		if mapping.Root {
			observation.rootID = mapping.ID
		}
	}
	if terminal && len(instance.NetworkInterfaces) == 0 {
		observation.pending = true
	} else if len(instance.NetworkInterfaces) != 1 {
		valid = false
	} else {
		nic := instance.NetworkInterfaces[0]
		valid = valid && nic.Attachment != nil && nic.Attachment.DeviceIndex != nil && aws.ToInt32(nic.Attachment.DeviceIndex) == 0 && aws.ToBool(nic.Attachment.DeleteOnTermination) && aws.ToString(nic.SubnetId) == choice.SubnetID && exactFleetGroup(nic.Groups, plan.SecurityGroupID)
		if nic.OwnerId != nil && aws.ToString(nic.OwnerId) != plan.Account {
			valid = false
		}
		public := aws.ToString(instance.PublicIpAddress)
		associated := ""
		if nic.Association != nil {
			associated = aws.ToString(nic.Association.PublicIp)
		}
		for _, value := range []string{public, associated} {
			if value != "" {
				address, err := netip.ParseAddr(value)
				valid = valid && err == nil && address.Is4() && address.IsGlobalUnicast() && !address.IsPrivate()
			}
		}
		if public == "" || associated == "" {
			// Public addressing can lag the synchronous allocation response and
			// disappear after teardown. Neither observation proves no allocation.
			observation.pending = true
		} else {
			valid = valid && public == associated
		}
	}
	observation.mismatch = observation.mismatch || !valid
	if valid {
		w.Name, _ = WorkerName(plan.BaseName, w.ID)
		w.RequestID, w.Profile, w.CreatedAt = plan.RequestID, plan.Profile, plan.CreatedAt
		w.Image, w.Type, w.Market = actual.Image, actual.Type, actual.Market
		w.TemplateID, w.TemplateVersion = plan.Image.LaunchTemplateID, plan.Image.LaunchTemplateVersion
		w.Group, w.BaseName, w.AttemptID = plan.Group, plan.BaseName, attempt.AttemptID
		w.SubnetID, w.AvailabilityZone = choice.SubnetID, choice.AvailabilityZone
	}
}

func observeFleetVolume(observation *fleetWorkerObservation, plan LaunchPlan, attempt AttemptReceipt, volume types.Volume) {
	_, tagOK := fleetObservedTags(volume.Tags, plan, attempt)
	valid := tagOK && aws.ToBool(volume.Encrypted) && volume.VolumeType == types.VolumeTypeGp3 && aws.ToInt32(volume.Size) == int32(plan.RootDisk.SizeGB) && aws.ToString(volume.AvailabilityZone) == observation.worker.AvailabilityZone
	if volume.OwnerId != nil && aws.ToString(volume.OwnerId) != plan.Account {
		valid = false
	}
	if len(volume.Attachments) != 1 {
		valid = false
	} else {
		attachment := volume.Attachments[0]
		valid = valid && aws.ToString(attachment.InstanceId) == observation.worker.ID && aws.ToString(attachment.Device) == plan.Image.RootDeviceName && aws.ToBool(attachment.DeleteOnTermination)
		if attachment.VolumeId != nil && aws.ToString(attachment.VolumeId) != observation.rootID {
			valid = false
		}
		if attachment.State != types.VolumeAttachmentStateAttached || volume.State != types.VolumeStateInUse {
			observation.pending = true
		}
	}
	observation.mismatch = observation.mismatch || !valid
	observation.rootVerified = valid
}

func fleetObservedTags(tags []types.Tag, plan LaunchPlan, attempt AttemptReceipt) (map[string]string, bool) {
	actual := map[string]string{}
	valid := true
	for _, tag := range tags {
		key := aws.ToString(tag.Key)
		if _, exists := actual[key]; exists || key == "" || tag.Value == nil {
			valid = false
		}
		actual[key] = aws.ToString(tag.Value)
	}
	expected, err := plan.AttemptTags(attempt.AttemptID)
	if err != nil {
		return actual, false
	}
	for key, value := range expected {
		if actual[key] != value {
			valid = false
		}
	}
	if plan.Group == "" {
		if _, present := actual["Group"]; present {
			valid = false
		}
	}
	return actual, valid
}

func exactFleetGroup(groups []types.GroupIdentifier, id string) bool {
	return len(groups) == 1 && aws.ToString(groups[0].GroupId) == id
}

func mergeFleetVolumes(known, observed []Volume) []Volume {
	result := append([]Volume{}, known...)
	for _, volume := range observed {
		found := false
		for i, previous := range result {
			if previous.ID == volume.ID && previous.Device == volume.Device && previous.Root == volume.Root {
				result[i], found = volume, true
				break
			}
		}
		if !found {
			result = append(result, volume)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Device < result[j].Device
	})
	return result
}
