package lifecycle

import (
	"context"
	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type UpOptions struct {
	Name, Resume string
	OnDemand     bool
}

func launchInput(r Receipt) *ec2.RunInstancesInput {
	p := r.Parameters
	tags := []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("devbox")}, {Key: aws.String("Deployment"), Value: aws.String(p.Deployment)}, {Key: aws.String("Owner"), Value: aws.String(p.Owner)}, {Key: aws.String("Profile"), Value: aws.String(p.Profile)}, {Key: aws.String("Name"), Value: aws.String(p.Name)}, {Key: aws.String("RequestId"), Value: aws.String(r.RequestID)}, {Key: aws.String("CreatedAt"), Value: aws.String(r.CreatedAt)}}
	return &ec2.RunInstancesInput{
		ClientToken: aws.String(r.ClientToken), MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
		ImageId: aws.String(p.Image.AMIID), InstanceType: types.InstanceType(p.InstanceType),
		LaunchTemplate:      &types.LaunchTemplateSpecification{LaunchTemplateId: aws.String(p.Image.LaunchTemplateID), Version: aws.String(p.Image.LaunchTemplateVersion)},
		MetadataOptions:     &types.InstanceMetadataOptionsRequest{HttpTokens: types.HttpTokensStateRequired},
		BlockDeviceMappings: []types.BlockDeviceMapping{{DeviceName: aws.String(p.Image.RootDeviceName), Ebs: &types.EbsBlockDevice{VolumeSize: aws.Int32(int32(p.DiskGB)), VolumeType: types.VolumeTypeGp3, Encrypted: aws.Bool(true), DeleteOnTermination: aws.Bool(true)}}},
		TagSpecifications:   []types.TagSpecification{{ResourceType: types.ResourceTypeInstance, Tags: tags}, {ResourceType: types.ResourceTypeVolume, Tags: tags}},
	}
}
func (s *Service) Up(ctx context.Context, m config.Manifest, p config.Profile, o UpOptions, store Store, announce func(Receipt, string) error) (Outcome, error) {
	result := Outcome{Instances: []Instance{}}
	var r Receipt
	var err error
	if o.Resume != "" {
		if o.Name != "" || o.OnDemand {
			return result, failure("replay_parameters_changed", "--resume cannot be combined with launch parameters; use up --resume REQUEST_ID")
		}
		r, err = store.Load(o.Resume)
	} else {
		return result, failure("legacy_request_no_expiry", "Legacy allocation is retired; create a new request through the expiry-capable Fleet path.")
	}
	if err != nil {
		return result, err
	}
	result.RequestID = r.RequestID
	result.ReceiptPath = store.Path(r.RequestID)
	unlock, err := store.Lock(ctx, r.RequestID)
	if err != nil {
		return result, err
	}
	defer unlock()
	if o.Resume != "" {
		r, err = store.Load(o.Resume)
		if err != nil {
			return result, err
		}
	}
	for _, id := range r.InstanceIDs {
		result.Instances = append(result.Instances, Instance{ID: id, State: "not_observed", SSM: "not_observed", Bootstrap: "not_observed", Readiness: "not_observed", RootDeletion: "unavailable", Volumes: []Volume{}})
	}
	rp := r.Parameters
	if rp.Account != s.Scope.ExpectedAccount || rp.Region != s.Scope.Region || rp.Deployment != s.Scope.Deployment || rp.Owner != s.Scope.Owner {
		return result, failure("scope_mismatch", "request receipt belongs to a different account/region/deployment/owner; select its original configuration")
	}
	if r.State != "prepared" {
		return s.reconcile(ctx, r, store, result)
	}
	// A prepared historical receipt can still expose workers accepted by an old
	// client. Observe those identities, but never send RunInstances again.
	found, err := s.requestMatches(ctx, r)
	if len(found) > 0 {
		result.Instances = found
	}
	if err != nil {
		return result, err
	}
	if len(found) > 0 {
		return s.finish(ctx, r, store, result, found)
	}
	return result, failure("legacy_request_no_expiry", "Legacy request has no expiry and cannot allocate; create a new request. Inspection and down remain available.")
}
func (s *Service) requestMatches(ctx context.Context, r Receipt) ([]Instance, error) {
	found, err := s.inventory(ctx, "", "", r.RequestID)
	if err != nil {
		return found, err
	}
	for _, i := range found {
		if i.clientToken != r.ClientToken {
			return found, failure("request_conflict", "request tag matches an unexpected EC2 client token; inspect AWS inventory; no further allocation performed")
		}
		if len(r.InstanceIDs) > 0 {
			matched := false
			for _, id := range r.InstanceIDs {
				if i.ID == id {
					matched = true
				}
			}
			if !matched {
				return found, failure("request_conflict", "request identity returned an unexpected instance ID; inspect AWS inventory")
			}
		}
	}
	return found, nil
}
func (s *Service) reconcile(ctx context.Context, r Receipt, store Store, result Outcome) (Outcome, error) {
	result.Status = "outcome_unresolved"
	for attempt := 0; attempt < 5; attempt++ {
		found, err := s.requestMatches(ctx, r)
		if len(found) > 0 {
			result.Instances = found
		}
		if err != nil {
			return result, err
		}
		if len(found) > 0 {
			return s.finish(ctx, r, store, result, found)
		}
		if attempt < 4 {
			if err = s.pause(ctx, attempt); err != nil {
				return result, err
			}
		}
	}
	if err := launchFailure(r.LaunchErrorCode); err != nil {
		return result, err
	}
	return result, failure("outcome_unresolved", "launch outcome remains unresolved; safely repeat up --resume with this request ID; use ls/down for inspection and cleanup; a new up is an independent allocation and is not a safe retry")
}
func (s *Service) finish(ctx context.Context, r Receipt, store Store, result Outcome, found []Instance) (Outcome, error) {
	result.Instances = found
	if len(found) != 1 {
		return result, failure("request_ambiguous", "multiple instances match this request; inspect all returned IDs and clean up by explicit ID; no further allocation performed")
	}
	r.State = "observed"
	r.LaunchErrorCode = ""
	r.InstanceIDs = []string{found[0].ID}
	if err := store.Save(r); err != nil {
		return result, err
	}
	if found[0].State == "terminated" {
		result.Status = "already_terminated"
		return result, nil
	}
	if found[0].State == "shutting-down" {
		result.Status = "shutting_down"
		return result, nil
	}
	p := r.Parameters
	i := found[0]
	if i.Image != p.Image.AMIID || i.Type != p.InstanceType || i.Market != "on-demand" || i.TemplateID != p.Image.LaunchTemplateID || i.TemplateVersion != p.Image.LaunchTemplateVersion {
		return result, failure("launch_identity_mismatch", "allocated instance does not expose the exact requested image/type/market/template; inspect the returned instance ID and clean up with down if necessary")
	}
	named, err := s.inventory(ctx, "", p.Name, "")
	if err != nil {
		return result, err
	}
	if len(active(named)) > 1 {
		result.Instances = active(named)
		return result, failure("name_ambiguous", "concurrent requests share this name; inspect all returned IDs and use down with an explicit ID")
	}
	result.Status = "allocated"
	return result, nil
}

// Messages describe the original launch response, not current account status.
// Reconciliation always takes precedence when AWS subsequently exposes an instance.
func launchFailure(code string) error {
	switch code {
	case "PendingVerification":
		return failure("launch_pending_verification", "EC2 reported pending account verification during the original launch; wait for AWS's confirmation email and contact AWS Support if validation remains pending; keep this request ID and use up --resume to reconcile before considering a separate launch; resume does not resubmit")
	case "UnauthorizedOperation", "AuthFailure", "IdempotentParameterMismatch", "InsufficientInstanceCapacity":
		return failure("launch_rejected", "EC2 rejected the original launch; check restricted operator permissions, original request parameters and capacity; resume this request to inspect its outcome before considering a separate launch")
	default:
		return nil
	}
}
