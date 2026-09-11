package lifecycle

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type Outcome struct {
	Status      string     `json:"status"`
	Instances   []Instance `json:"instances"`
	RequestID   string     `json:"request_id,omitempty"`
	ReceiptPath string     `json:"receipt_path,omitempty"`
}

func (s *Service) Down(ctx context.Context, target string) (Outcome, error) {
	result := Outcome{Instances: []Instance{}}
	if !ValidTarget(target) {
		return result, failure("target_invalid", "use one friendly name or EC2 instance ID")
	}
	id, name := "", target
	if instanceRE.MatchString(target) {
		id = target
		name = ""
	}
	found, err := s.inventory(ctx, id, name, "")
	if err != nil {
		return result, err
	}
	if len(found) == 0 {
		result.Status = "no_managed_match"
		return result, nil
	}
	live := active(found)
	if len(live) > 1 {
		result.Instances = live
		return result, failure("name_ambiguous", "multiple managed instances share this name; inspect the returned IDs and run down with one explicit ID")
	}
	if len(live) == 0 {
		result.Status = "already_terminated"
		result.Instances = found
		// Historical matches are observations, never a new termination target.
		for n := range result.Instances {
			if err = s.observeVolumes(ctx, &result.Instances[n]); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	chosen := live[0]
	result.Instances = []Instance{chosen}
	// A fresh ID read closes the lookup-to-mutation gap as far as EC2 permits.
	current, err := s.inventory(ctx, chosen.ID, "", "")
	if err != nil {
		return result, err
	}
	if len(current) != 1 {
		return result, failure("target_unresolved", "selected instance is no longer visible; termination is unverified; retry down with the returned ID")
	}
	chosen = current[0]
	result.Instances[0] = chosen
	if name != "" && chosen.Name != name {
		return result, failure("scope_mismatch", "instance name changed during lookup; inspect inventory before retrying by explicit ID")
	}
	if chosen.State == "terminated" {
		result.Status = "already_terminated"
		return result, s.observeVolumes(ctx, &result.Instances[0])
	}
	switch chosen.State {
	case "pending", "running", "stopping", "stopped", "shutting-down":
	default:
		return result, failure("state_unknown", "instance state is unavailable; inspect the returned ID before retrying cleanup")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result.Status = "termination_requested"
	if chosen.State != "shutting-down" {
		// Even an error may mean AWS accepted the termination. Preserve the target and
		// poll; never infer success from the mutation response alone.
		_, terminationErr := s.API.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{chosen.ID}})
		if apiCode(terminationErr, "UnauthorizedOperation") || apiCode(terminationErr, "AuthFailure") {
			return result, failure("termination_denied", "EC2 denied termination; refresh credentials and check restricted operator termination permissions for the returned instance ID")
		}
	}
	for attempt := 0; attempt < 30; attempt++ {
		current, err = s.inventory(ctx, chosen.ID, "", "")
		if err != nil {
			return result, err
		}
		if len(current) == 1 {
			// Retain the pre-termination mappings; EC2 may omit them after termination.
			result.Instances[0].State = current[0].State
			if current[0].State == "terminated" {
				result.Status = "terminated"
				return result, s.observeVolumes(ctx, &result.Instances[0])
			}
		}
		if err = s.pause(ctx, attempt); err != nil {
			return result, err
		}
	}
	return result, failure("termination_unresolved", "termination was not observed within the retry bound; inspect the returned instance and volume IDs and retry down by ID")
}
func (s *Service) observeVolumes(ctx context.Context, instance *Instance) error {
	for n := range instance.Volumes {
		v := &instance.Volumes[n]
		if !v.DeleteOnTermination {
			continue
		}
		deleted := false
		for attempt := 0; attempt < 30; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			out, err := s.API.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{v.ID}})
			if apiCode(err, "InvalidVolume.NotFound") {
				deleted = true
				break
			}
			if err != nil || out == nil {
				return failure("volume_unresolved", "EC2 termination was observed but volume deletion could not be verified; inspect the returned volume IDs with DescribeVolumes")
			}
			if len(out.Volumes) != 1 || aws.ToString(out.Volumes[0].VolumeId) != v.ID {
				return failure("volume_unresolved", "volume lookup returned no exact match; deletion remains unverified; inspect the returned volume ID")
			}
			if out.Volumes[0].State == types.VolumeStateDeleted {
				deleted = true
				break
			}
			if err = s.pause(ctx, attempt); err != nil {
				return err
			}
		}
		if deleted {
			v.Deletion = "deleted"
			if v.Root {
				instance.RootDeletion = "deleted"
			}
		} else {
			return failure("volume_unresolved", "EC2 terminated but volume deletion was not observed within the retry bound; inspect the returned volume IDs")
		}
	}
	return nil
}
