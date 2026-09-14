package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type fleetInstancesPage struct {
	out *ec2.DescribeInstancesOutput
	err error
}

type fleetVolumesPage struct {
	out *ec2.DescribeVolumesOutput
	err error
}

type fleetInventoryStub struct {
	instances      []fleetInstancesPage
	volumes        []fleetVolumesPage
	instanceInputs []ec2.DescribeInstancesInput
	volumeInputs   []ec2.DescribeVolumesInput
}

func (s *fleetInventoryStub) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	copy := *in
	copy.InstanceIds = append([]string{}, in.InstanceIds...)
	if in.NextToken != nil {
		copy.NextToken = aws.String(*in.NextToken)
	}
	i := len(s.instanceInputs)
	s.instanceInputs = append(s.instanceInputs, copy)
	if i >= len(s.instances) {
		return nil, errors.New("unexpected DescribeInstances call")
	}
	return s.instances[i].out, s.instances[i].err
}

func (s *fleetInventoryStub) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	copy := *in
	copy.VolumeIds = append([]string{}, in.VolumeIds...)
	if in.NextToken != nil {
		copy.NextToken = aws.String(*in.NextToken)
	}
	i := len(s.volumeInputs)
	s.volumeInputs = append(s.volumeInputs, copy)
	if i >= len(s.volumes) {
		return nil, errors.New("unexpected DescribeVolumes call")
	}
	return s.volumes[i].out, s.volumes[i].err
}

func fleetWorkersFixture(t *testing.T, onDemand bool) (LaunchPlan, AttemptReceipt, []WorkerOutcome, *fleetInventoryStub) {
	t.Helper()
	plan, attempt := fleetWireFixture(t, onDemand)
	attempt.InstanceIDs = []string{"i-0123456789abcdef0", "i-0123456789abcdef1"}
	known := make([]WorkerOutcome, 0, 2)
	instances := make([]types.Instance, 0, 2)
	volumes := make([]types.Volume, 0, 2)
	for n, id := range attempt.InstanceIDs {
		choice := plan.Choices[n]
		known = append(known, fleetWorker(plan, attempt, id, choice))
		volumeID := "vol-0123456789abcdef" + string(rune('0'+n))
		tagMap, err := plan.AttemptTags(attempt.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(tagMap))
		for key := range tagMap {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		tags := make([]types.Tag, 0, len(keys))
		for _, key := range keys {
			tags = append(tags, types.Tag{Key: aws.String(key), Value: aws.String(tagMap[key])})
		}
		instanceTags := append(append([]types.Tag{}, tags...), types.Tag{Key: aws.String("aws:ec2launchtemplate:id"), Value: aws.String(plan.Image.LaunchTemplateID)}, types.Tag{Key: aws.String("aws:ec2launchtemplate:version"), Value: aws.String(plan.Image.LaunchTemplateVersion)})
		market := types.InstanceLifecycleTypeSpot
		if onDemand {
			market = ""
		}
		groups := []types.GroupIdentifier{{GroupId: aws.String(plan.SecurityGroupID)}}
		instances = append(instances, types.Instance{
			InstanceId: aws.String(id), ImageId: aws.String(plan.Image.AMIID), InstanceType: types.InstanceType(choice.InstanceType),
			InstanceLifecycle: market, Tags: instanceTags, State: &types.InstanceState{Name: types.InstanceStateNameRunning},
			SubnetId: aws.String(choice.SubnetID), Placement: &types.Placement{AvailabilityZone: aws.String(choice.AvailabilityZone)},
			IamInstanceProfile: &types.IamInstanceProfile{Arn: aws.String(plan.InstanceProfileARN)}, SecurityGroups: groups,
			MetadataOptions: &types.InstanceMetadataOptionsResponse{HttpTokens: types.HttpTokensStateRequired, HttpEndpoint: types.InstanceMetadataEndpointStateEnabled, HttpPutResponseHopLimit: aws.Int32(1), InstanceMetadataTags: types.InstanceMetadataTagsStateDisabled, State: types.InstanceMetadataOptionsStateApplied},
			RootDeviceName:  aws.String(plan.Image.RootDeviceName), RootDeviceType: types.DeviceTypeEbs,
			BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String(plan.Image.RootDeviceName), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String(volumeID), DeleteOnTermination: aws.Bool(true)}}},
			PublicIpAddress:     aws.String("203.0.113.10"),
			NetworkInterfaces:   []types.InstanceNetworkInterface{{SubnetId: aws.String(choice.SubnetID), OwnerId: aws.String(plan.Account), Groups: append([]types.GroupIdentifier{}, groups...), Attachment: &types.InstanceNetworkInterfaceAttachment{DeviceIndex: aws.Int32(0), DeleteOnTermination: aws.Bool(true)}, Association: &types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("203.0.113.10")}}},
			// Fleet does not promise to propagate its client token to instances.
			ClientToken: aws.String("unrelated-instance-token"),
		})
		volumes = append(volumes, types.Volume{
			VolumeId: aws.String(volumeID), OwnerId: aws.String(plan.Account), VolumeType: types.VolumeTypeGp3, Size: aws.Int32(137), Encrypted: aws.Bool(true),
			AvailabilityZone: aws.String(choice.AvailabilityZone), State: types.VolumeStateInUse, Tags: append([]types.Tag{}, tags...),
			Attachments: []types.VolumeAttachment{{InstanceId: aws.String(id), VolumeId: aws.String(volumeID), Device: aws.String(plan.Image.RootDeviceName), DeleteOnTermination: aws.Bool(true), State: types.VolumeAttachmentStateAttached}},
		})
	}
	stub := &fleetInventoryStub{
		instances: []fleetInstancesPage{{out: &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(plan.Account), Instances: instances}}}}},
		volumes:   []fleetVolumesPage{{out: &ec2.DescribeVolumesOutput{Volumes: volumes}}},
	}
	return plan, attempt, known, stub
}

func TestVerifyFleetWorkersUsesExactIDsAndActualPins(t *testing.T) {
	for _, onDemand := range []bool{false, true} {
		t.Run(map[bool]string{false: "spot", true: "on-demand"}[onDemand], func(t *testing.T) {
			plan, attempt, known, api := fleetWorkersFixture(t, onDemand)
			// Both API result order and caller order may differ from instance ID order.
			known[0], known[1] = known[1], known[0]
			instances := api.instances[0].out.Reservations[0].Instances
			instances[0], instances[1] = instances[1], instances[0]
			api.volumes[0].out.Volumes[0], api.volumes[0].out.Volumes[1] = api.volumes[0].out.Volumes[1], api.volumes[0].out.Volumes[0]
			before, _ := json.Marshal(known)
			workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
			if err != nil || len(workers) != 2 {
				t.Fatalf("verification: workers=%+v err=%v", workers, err)
			}
			for n, worker := range workers {
				wantName, _ := WorkerName(plan.BaseName, attempt.InstanceIDs[n])
				if worker.ID != attempt.InstanceIDs[n] || worker.Name != wantName || worker.Status != "allocated" || worker.ObservationCode != "" || len(worker.Volumes) != 1 || !worker.Volumes[0].Root || !worker.Volumes[0].DeleteOnTermination {
					t.Fatalf("unverified worker: %+v", worker)
				}
			}
			if !reflect.DeepEqual(api.instanceInputs[0].InstanceIds, attempt.InstanceIDs) || len(api.instanceInputs[0].Filters) != 0 || api.instanceInputs[0].MaxResults != nil {
				t.Fatalf("instance inspection was not by exact IDs: %+v", api.instanceInputs)
			}
			if !reflect.DeepEqual(api.volumeInputs[0].VolumeIds, []string{"vol-0123456789abcdef0", "vol-0123456789abcdef1"}) || len(api.volumeInputs[0].Filters) != 0 || api.volumeInputs[0].MaxResults != nil {
				t.Fatalf("volume inspection was not by exact IDs: %+v", api.volumeInputs)
			}
			after, _ := json.Marshal(known)
			if string(before) != string(after) {
				t.Fatal("verification mutated the supplied known observations")
			}
		})
	}
}

func changeFleetTestTag(tags []types.Tag, key, value string) {
	for i := range tags {
		if aws.ToString(tags[i].Key) == key {
			tags[i].Value = aws.String(value)
		}
	}
}

func TestVerifyFleetWorkersRejectsIdentityAndRootMismatches(t *testing.T) {
	for _, problem := range []string{
		"owner", "managed-by", "deployment", "owner-tag", "request", "attempt", "profile", "group", "base-name", "name", "naming-version", "created-at", "batch", "duplicate-tag", "image", "template", "template-version", "type", "subnet", "zone", "market", "instance-profile", "security-group", "extra-security-group", "metadata-tokens", "metadata-endpoint", "metadata-hops", "metadata-tags", "root-device", "root-delete", "root-type", "extra-disk", "nic-count", "nic-subnet", "nic-owner", "nic-group", "nic-index", "nic-delete", "public-ip", "public-ip-mismatch", "volume-size", "volume-type", "volume-encryption", "volume-owner", "volume-zone", "volume-tags", "volume-instance", "volume-device", "volume-delete", "extra-attachment",
	} {
		t.Run(problem, func(t *testing.T) {
			plan, attempt, known, api := fleetWorkersFixture(t, false)
			i := &api.instances[0].out.Reservations[0].Instances[0]
			v := &api.volumes[0].out.Volumes[0]
			switch problem {
			case "owner":
				api.instances[0].out.Reservations[0].OwnerId = aws.String("999999999999")
			case "managed-by":
				changeFleetTestTag(i.Tags, "ManagedBy", "other")
			case "deployment":
				changeFleetTestTag(i.Tags, "Deployment", "other")
			case "owner-tag":
				changeFleetTestTag(i.Tags, "Owner", "other")
			case "request":
				changeFleetTestTag(i.Tags, "RequestId", strings.Repeat("b", 32))
			case "attempt":
				changeFleetTestTag(i.Tags, "AttemptId", strings.Repeat("b", 32))
			case "profile":
				changeFleetTestTag(i.Tags, "Profile", "other")
			case "group":
				changeFleetTestTag(i.Tags, "Group", "other")
			case "base-name":
				changeFleetTestTag(i.Tags, "BaseName", "other")
			case "name":
				changeFleetTestTag(i.Tags, "Name", "other")
			case "naming-version":
				changeFleetTestTag(i.Tags, "NamingVersion", "2")
			case "created-at":
				changeFleetTestTag(i.Tags, "CreatedAt", "2020-01-01T00:00:00Z")
			case "batch":
				changeFleetTestTag(i.Tags, "BatchId", strings.Repeat("b", 32))
			case "duplicate-tag":
				i.Tags = append(i.Tags, i.Tags[0])
			case "image":
				i.ImageId = aws.String("ami-87654321")
			case "template":
				changeFleetTestTag(i.Tags, "aws:ec2launchtemplate:id", "lt-87654321")
			case "template-version":
				changeFleetTestTag(i.Tags, "aws:ec2launchtemplate:version", "$Latest")
			case "type":
				i.InstanceType = "c8g.2xlarge"
			case "subnet":
				i.SubnetId = aws.String("subnet-11111111")
			case "zone":
				i.Placement.AvailabilityZone = aws.String("us-east-2c")
			case "market":
				i.InstanceLifecycle = ""
			case "instance-profile":
				i.IamInstanceProfile.Arn = aws.String("arn:aws:iam::123456789012:instance-profile/other")
			case "security-group":
				i.SecurityGroups[0].GroupId = aws.String("sg-11111111")
			case "extra-security-group":
				i.SecurityGroups = append(i.SecurityGroups, i.SecurityGroups[0])
			case "metadata-tokens":
				i.MetadataOptions.HttpTokens = types.HttpTokensStateOptional
			case "metadata-endpoint":
				i.MetadataOptions.HttpEndpoint = types.InstanceMetadataEndpointStateDisabled
			case "metadata-hops":
				i.MetadataOptions.HttpPutResponseHopLimit = aws.Int32(2)
			case "metadata-tags":
				i.MetadataOptions.InstanceMetadataTags = types.InstanceMetadataTagsStateEnabled
			case "root-device":
				i.RootDeviceName = aws.String("/dev/sdb")
			case "root-delete":
				i.BlockDeviceMappings[0].Ebs.DeleteOnTermination = aws.Bool(false)
			case "root-type":
				i.RootDeviceType = types.DeviceTypeInstanceStore
			case "extra-disk":
				i.BlockDeviceMappings = append(i.BlockDeviceMappings, types.InstanceBlockDeviceMapping{DeviceName: aws.String("/dev/sdb"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-88888888"), DeleteOnTermination: aws.Bool(true)}})
			case "nic-count":
				i.NetworkInterfaces = append(i.NetworkInterfaces, i.NetworkInterfaces[0])
			case "nic-subnet":
				i.NetworkInterfaces[0].SubnetId = aws.String("subnet-11111111")
			case "nic-owner":
				i.NetworkInterfaces[0].OwnerId = aws.String("999999999999")
			case "nic-group":
				i.NetworkInterfaces[0].Groups[0].GroupId = aws.String("sg-11111111")
			case "nic-index":
				i.NetworkInterfaces[0].Attachment.DeviceIndex = aws.Int32(1)
			case "nic-delete":
				i.NetworkInterfaces[0].Attachment.DeleteOnTermination = aws.Bool(false)
			case "public-ip":
				i.PublicIpAddress, i.NetworkInterfaces[0].Association.PublicIp = aws.String("10.0.0.1"), aws.String("10.0.0.1")
			case "public-ip-mismatch":
				i.NetworkInterfaces[0].Association.PublicIp = aws.String("203.0.113.11")
			case "volume-size":
				v.Size = aws.Int32(100)
			case "volume-type":
				v.VolumeType = types.VolumeTypeGp2
			case "volume-encryption":
				v.Encrypted = aws.Bool(false)
			case "volume-owner":
				v.OwnerId = aws.String("999999999999")
			case "volume-zone":
				v.AvailabilityZone = aws.String("us-east-2c")
			case "volume-tags":
				changeFleetTestTag(v.Tags, "AttemptId", strings.Repeat("b", 32))
			case "volume-instance":
				v.Attachments[0].InstanceId = aws.String(attempt.InstanceIDs[1])
			case "volume-device":
				v.Attachments[0].Device = aws.String("/dev/sdb")
			case "volume-delete":
				v.Attachments[0].DeleteOnTermination = aws.Bool(false)
			case "extra-attachment":
				v.Attachments = append(v.Attachments, v.Attachments[0])
			}
			workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
			if err == nil || len(workers) != 2 || workers[0].ID != attempt.InstanceIDs[0] || workers[0].Status != "identity_mismatch" || len(workers[0].Volumes) == 0 {
				t.Fatalf("mismatch lost evidence or passed: workers=%+v err=%v", workers, err)
			}
		})
	}
}

func TestVerifyFleetWorkersWaitsForStartupObservations(t *testing.T) {
	for _, pending := range []string{"public-address", "nic-association", "metadata", "attachment"} {
		t.Run(pending, func(t *testing.T) {
			plan, attempt, known, api := fleetWorkersFixture(t, false)
			i := &api.instances[0].out.Reservations[0].Instances[0]
			switch pending {
			case "public-address":
				i.PublicIpAddress = nil
			case "nic-association":
				i.NetworkInterfaces[0].Association = nil
			case "metadata":
				i.MetadataOptions.State = types.InstanceMetadataOptionsStatePending
			case "attachment":
				api.volumes[0].out.Volumes[0].Attachments[0].State = types.VolumeAttachmentStateAttaching
			}
			workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
			if err == nil || len(workers) != 2 || workers[0].Status != "not_observed" || workers[1].Status != "allocated" || len(workers[0].Volumes) != 1 {
				t.Fatalf("startup lag changed allocation evidence: workers=%+v err=%v", workers, err)
			}
		})
	}
}

func TestVerifyFleetWorkersPaginationAndFailuresPreserveEveryIdentity(t *testing.T) {
	for _, behavior := range []string{"pagination", "instance-error", "instance-partial-error", "instance-page-error", "instance-missing", "instance-cycle", "instance-duplicate", "instance-unexpected", "volume-error", "volume-partial-error", "volume-missing", "volume-cycle", "volume-duplicate", "volume-unexpected"} {
		t.Run(behavior, func(t *testing.T) {
			plan, attempt, known, api := fleetWorkersFixture(t, false)
			instanceSet := api.instances[0].out.Reservations[0].Instances
			volumeSet := api.volumes[0].out.Volumes
			instancePage := func(instances []types.Instance, token string) *ec2.DescribeInstancesOutput {
				return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(plan.Account), Instances: instances}}, NextToken: aws.String(token)}
			}
			volumePage := func(volumes []types.Volume, token string) *ec2.DescribeVolumesOutput {
				return &ec2.DescribeVolumesOutput{Volumes: volumes, NextToken: aws.String(token)}
			}
			secretErr := errors.New("SECRET provider detail")
			switch behavior {
			case "pagination":
				api.instances = []fleetInstancesPage{{out: instancePage(instanceSet[:1], "next-instances")}, {out: instancePage(instanceSet[1:], "")}}
				api.volumes = []fleetVolumesPage{{out: volumePage(volumeSet[:1], "next-volumes")}, {out: volumePage(volumeSet[1:], "")}}
			case "instance-error":
				api.instances = []fleetInstancesPage{{err: secretErr}}
			case "instance-partial-error":
				api.instances = []fleetInstancesPage{{out: instancePage(instanceSet[:1], ""), err: secretErr}}
				api.volumes[0].out.Volumes = volumeSet[:1]
			case "instance-page-error":
				api.instances = []fleetInstancesPage{{out: instancePage(instanceSet[:1], "next")}, {err: secretErr}}
				api.volumes[0].out.Volumes = volumeSet[:1]
			case "instance-missing":
				api.instances[0].out = instancePage(instanceSet[:1], "")
				api.volumes[0].out.Volumes = volumeSet[:1]
			case "instance-cycle":
				api.instances = []fleetInstancesPage{{out: instancePage(instanceSet, "repeat")}, {out: instancePage(nil, "repeat")}}
			case "instance-duplicate":
				api.instances = []fleetInstancesPage{{out: instancePage(instanceSet, "next")}, {out: instancePage(instanceSet[:1], "")}}
			case "instance-unexpected":
				extra := instanceSet[0]
				extra.InstanceId = aws.String("i-88888888")
				api.instances[0].out = instancePage(append(instanceSet, extra), "")
			case "volume-error":
				api.volumes = []fleetVolumesPage{{err: secretErr}}
			case "volume-partial-error":
				api.volumes = []fleetVolumesPage{{out: volumePage(volumeSet[:1], ""), err: secretErr}}
			case "volume-missing":
				api.volumes[0].out.Volumes = volumeSet[:1]
			case "volume-cycle":
				api.volumes = []fleetVolumesPage{{out: volumePage(volumeSet, "repeat")}, {out: volumePage(nil, "repeat")}}
			case "volume-duplicate":
				api.volumes = []fleetVolumesPage{{out: volumePage(volumeSet, "next")}, {out: volumePage(volumeSet[:1], "")}}
			case "volume-unexpected":
				extra := volumeSet[0]
				extra.VolumeId = aws.String("vol-88888888")
				api.volumes[0].out.Volumes = append(volumeSet, extra)
			}
			workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
			if len(workers) != 2 || workers[0].ID != attempt.InstanceIDs[0] || workers[1].ID != attempt.InstanceIDs[1] {
				t.Fatalf("read failure erased known IDs: %+v", workers)
			}
			if behavior == "pagination" {
				if err != nil || workers[0].Status != "allocated" || workers[1].Status != "allocated" {
					t.Fatalf("pagination failed: workers=%+v err=%v", workers, err)
				}
				if aws.ToString(api.instanceInputs[1].NextToken) != "next-instances" || !reflect.DeepEqual(api.instanceInputs[1].InstanceIds, attempt.InstanceIDs) || aws.ToString(api.volumeInputs[1].NextToken) != "next-volumes" || !reflect.DeepEqual(api.volumeInputs[0].VolumeIds, api.volumeInputs[1].VolumeIds) {
					t.Fatal("pagination changed exact requested identities or lost next token")
				}
			} else if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("failed inspection passed or leaked provider detail: %v", err)
			}
			if strings.HasPrefix(behavior, "volume-") && (len(workers[0].Volumes) != 1 || len(workers[1].Volumes) != 1) {
				t.Fatalf("volume read failure erased learned mappings: %+v", workers)
			}
			if strings.HasPrefix(behavior, "instance-") && behavior != "instance-error" && len(workers[0].Volumes) != 1 {
				t.Fatalf("partial instance read erased learned mapping: %+v", workers)
			}
		})
	}
}

func TestVerifyFleetWorkersPreservesHistoricalMappingsAndAttemptIDs(t *testing.T) {
	plan, attempt, known, api := fleetWorkersFixture(t, false)
	known[0].Volumes = []Volume{{ID: "vol-88888888", Device: plan.Image.RootDeviceName, Root: true, DeleteOnTermination: true, Deletion: "not_observed"}}
	// The immutable attempt independently retains the other worker's identity.
	workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known[:1])
	if err == nil || len(workers) != 2 || workers[0].Status != "identity_mismatch" || workers[1].Status != "allocated" || len(workers[0].Volumes) != 2 {
		t.Fatalf("changed root or missing cached worker lost evidence: workers=%+v err=%v", workers, err)
	}
	if known[0].Volumes[0].ID != "vol-88888888" || len(known[0].Volumes) != 1 {
		t.Fatal("historical observation was mutated")
	}
	for _, unavailable := range []string{"canceled", "no-client", "missing-inventory"} {
		t.Run(unavailable, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var client FleetInventory = api
			switch unavailable {
			case "canceled":
				cancel()
			case "no-client":
				client = nil
			case "missing-inventory":
				client = &fleetInventoryStub{instances: []fleetInstancesPage{{out: &ec2.DescribeInstancesOutput{}}}}
			}
			workers, err := VerifyFleetWorkers(ctx, client, plan, attempt, known)
			if err == nil || len(workers) != 2 || workers[0].Status != "not_observed" || len(workers[0].Volumes) != 1 || workers[0].Volumes[0].ID != "vol-88888888" {
				t.Fatalf("unavailable read erased original identity/root: workers=%+v err=%v", workers, err)
			}
			if unavailable == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestVerifyFleetWorkersRequiresGroupAbsenceWhenUngrouped(t *testing.T) {
	plan, attempt, known, api := fleetWorkersFixture(t, false)
	plan.Group = ""
	delete(plan.CreationTags, "Group")
	removeGroup := func(tags []types.Tag) []types.Tag {
		result := []types.Tag{}
		for _, tag := range tags {
			if aws.ToString(tag.Key) != "Group" {
				result = append(result, tag)
			}
		}
		return result
	}
	for i := range api.instances[0].out.Reservations[0].Instances {
		instance := &api.instances[0].out.Reservations[0].Instances[i]
		instance.Tags = removeGroup(instance.Tags)
		api.volumes[0].out.Volumes[i].Tags = removeGroup(api.volumes[0].out.Volumes[i].Tags)
	}
	workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
	if err != nil || workers[0].Group != "" || workers[1].Group != "" {
		t.Fatalf("ungrouped workers rejected: workers=%+v err=%v", workers, err)
	}
	api.instanceInputs, api.volumeInputs = nil, nil
	api.instances[0].out.Reservations[0].Instances[0].Tags = append(api.instances[0].out.Reservations[0].Instances[0].Tags, types.Tag{Key: aws.String("Group"), Value: aws.String("")})
	workers, err = VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
	if err == nil || workers[0].Status != "identity_mismatch" {
		t.Fatalf("unexpected Group tag accepted: workers=%+v err=%v", workers, err)
	}
}

func TestVerifyFleetWorkersTeardownDoesNotEraseHistoricalFulfillment(t *testing.T) {
	plan, attempt, known, api := fleetWorkersFixture(t, false)
	for n := range known {
		known[n].Volumes = []Volume{{ID: aws.ToString(api.volumes[0].out.Volumes[n].VolumeId), Device: plan.Image.RootDeviceName, Root: true, DeleteOnTermination: true, Deletion: "not_observed"}}
	}
	i := &api.instances[0].out.Reservations[0].Instances[0]
	i.State.Name = types.InstanceStateNameTerminated
	i.BlockDeviceMappings, i.NetworkInterfaces, i.SecurityGroups = nil, nil, nil
	i.MetadataOptions, i.IamInstanceProfile, i.PublicIpAddress = nil, nil, nil
	api.volumes[0].out.Volumes = api.volumes[0].out.Volumes[1:]
	before, _ := json.Marshal(attempt)
	workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
	if err == nil || len(workers) != 2 || workers[0].Status != "not_observed" || workers[0].State != "terminated" || len(workers[0].Volumes) != 1 || workers[1].Status != "allocated" {
		t.Fatalf("teardown erased fulfillment or became an identity mismatch: workers=%+v err=%v", workers, err)
	}
	after, _ := json.Marshal(attempt)
	if string(before) != string(after) || !reflect.DeepEqual(attempt.InstanceIDs, []string{"i-0123456789abcdef0", "i-0123456789abcdef1"}) {
		t.Fatal("current resource absence rewrote original fulfillment")
	}
}

func TestVerifyFleetWorkersBindsReportedPoolWithinApprovedChoices(t *testing.T) {
	for _, change := range []string{"type", "subnet-and-zone", "fill-missing"} {
		t.Run(change, func(t *testing.T) {
			plan, attempt, known, api := fleetWorkersFixture(t, false)
			changed := 0
			switch change {
			case "type":
				// Both c6i and c7i are approved in subnet b, but the original
				// response identified this particular worker as c6i.
				api.instances[0].out.Reservations[0].Instances[0].InstanceType = "c7i.2xlarge"
			case "subnet-and-zone":
				changed = 1
				i := &api.instances[0].out.Reservations[0].Instances[1]
				i.SubnetId, i.NetworkInterfaces[0].SubnetId = aws.String("subnet-87654321"), aws.String("subnet-87654321")
				i.Placement.AvailabilityZone = aws.String("us-east-2b")
				api.volumes[0].out.Volumes[1].AvailabilityZone = aws.String("us-east-2b")
			case "fill-missing":
				for i := range known {
					known[i].Type, known[i].SubnetID, known[i].AvailabilityZone = "", "", ""
				}
			}
			workers, err := VerifyFleetWorkers(context.Background(), api, plan, attempt, known)
			if change == "fill-missing" {
				if err != nil || workers[0].Status != "allocated" || workers[1].Status != "allocated" || workers[0].Type == "" || workers[0].SubnetID == "" || workers[0].AvailabilityZone == "" {
					t.Fatalf("missing response placement was not filled: workers=%+v err=%v", workers, err)
				}
			} else if err == nil || len(workers) != 2 || workers[changed].Status != "identity_mismatch" || workers[changed].Type != known[changed].Type || workers[changed].SubnetID != known[changed].SubnetID || workers[changed].AvailabilityZone != known[changed].AvailabilityZone {
				t.Fatalf("changed approved placement overwrote response pins: workers=%+v err=%v", workers, err)
			}
		})
	}
}
