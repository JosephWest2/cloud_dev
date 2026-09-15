package lifecycle

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type Volume struct {
	ID                  string `json:"volume_id"`
	Device              string `json:"device"`
	Root                bool   `json:"root"`
	DeleteOnTermination bool   `json:"delete_on_termination"`
	Deletion            string `json:"deletion"`
}
type Instance struct {
	ID               string   `json:"instance_id"`
	Name             string   `json:"name"`
	RequestID        string   `json:"request_id"`
	Profile          string   `json:"profile"`
	CreatedAt        string   `json:"created_at"`
	Image            string   `json:"image_id"`
	Type             string   `json:"instance_type"`
	Market           string   `json:"market"`
	TemplateID       string   `json:"launch_template_id"`
	TemplateVersion  string   `json:"launch_template_version"`
	State            string   `json:"ec2_state"`
	SSM              string   `json:"ssm"`
	Bootstrap        string   `json:"bootstrap"`
	Readiness        string   `json:"readiness"`
	RootDeletion     string   `json:"root_volume_deletion"`
	Volumes          []Volume `json:"volumes"`
	ObservationCode  string   `json:"observation_code,omitempty"`
	ProbeCommandID   string   `json:"probe_command_id,omitempty"`
	Group            string   `json:"group,omitempty"`
	BaseName         string   `json:"base_name"`
	AttemptID        string   `json:"attempt_id"`
	SubnetID         string   `json:"subnet_id"`
	AvailabilityZone string   `json:"availability_zone"`
	HostKey          string   `json:"-"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
	ExpiryStatus     string   `json:"expiry_status,omitempty"`
	clientToken      string
}

func tagsOf(i types.Instance) map[string]string {
	tags := map[string]string{}
	for _, t := range i.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return tags
}
func (s *Service) scoped(i types.Instance) bool {
	t, valid := inventoryTags(i)
	return valid && t["ManagedBy"] == "devbox" && t["Deployment"] == s.Scope.Deployment && t["Owner"] == s.Scope.Owner
}
func record(i types.Instance) Instance {
	t := tagsOf(i)
	r := Instance{ID: aws.ToString(i.InstanceId), Name: t["Name"], RequestID: t["RequestId"], Profile: t["Profile"], CreatedAt: t["CreatedAt"], Image: aws.ToString(i.ImageId), Type: string(i.InstanceType), Market: "on-demand", TemplateID: t["aws:ec2launchtemplate:id"], TemplateVersion: t["aws:ec2launchtemplate:version"], State: "unknown", SSM: "not_observed", Bootstrap: "not_observed", Readiness: "not_observed", RootDeletion: "unavailable", Volumes: []Volume{}, clientToken: aws.ToString(i.ClientToken)}
	r.Group, r.BaseName, r.AttemptID, r.SubnetID = t["Group"], t["BaseName"], t["AttemptId"], aws.ToString(i.SubnetId)
	if i.Placement != nil {
		r.AvailabilityZone = aws.ToString(i.Placement.AvailabilityZone)
	}
	if t["NamingVersion"] == "1" {
		r.Name, _ = WorkerName(r.BaseName, r.ID)
	}
	if i.InstanceLifecycle != "" {
		r.Market = string(i.InstanceLifecycle)
	}
	if i.State != nil {
		r.State = string(i.State.Name)
	}
	for _, b := range i.BlockDeviceMappings {
		if b.Ebs == nil || !volumeRE.MatchString(aws.ToString(b.Ebs.VolumeId)) {
			continue
		}
		v := Volume{ID: aws.ToString(b.Ebs.VolumeId), Device: aws.ToString(b.DeviceName), Root: aws.ToString(b.DeviceName) == aws.ToString(i.RootDeviceName), DeleteOnTermination: aws.ToBool(b.Ebs.DeleteOnTermination), Deletion: "not_observed"}
		if !v.DeleteOnTermination {
			v.Deletion = "retained"
		}
		r.Volumes = append(r.Volumes, v)
		if v.Root {
			r.RootDeletion = v.Deletion
		}
	}
	return r
}

// Duplicate tags are invalid even when their values happen to agree. Converting
// them directly to a map would let response ordering decide the trusted scope.
func inventoryTags(i types.Instance) (map[string]string, bool) {
	tags := map[string]string{}
	for _, tag := range i.Tags {
		if tag.Key != nil && *tag.Key == "ExpiresAt" {
			continue
		}
		if tag.Key == nil || tag.Value == nil || *tag.Key == "" {
			return nil, false
		}
		if _, exists := tags[*tag.Key]; exists {
			return nil, false
		}
		tags[*tag.Key] = *tag.Value
	}
	return tags, true
}

func validInventoryIdentity(i types.Instance, tags map[string]string) bool {
	if !instanceRE.MatchString(aws.ToString(i.InstanceId)) || (tags["Name"] != "" && !ValidName(tags["Name"])) ||
		(tags["Group"] != "" && !ValidGroup(tags["Group"])) || (tags["RequestId"] != "" && !ValidRequest(tags["RequestId"])) ||
		(tags["Profile"] != "" && !ValidName(tags["Profile"])) {
		return false
	}
	if tags["CreatedAt"] != "" {
		if _, err := time.Parse(time.RFC3339Nano, tags["CreatedAt"]); err != nil {
			return false
		}
	}
	version, versioned := tags["NamingVersion"]
	if !versioned {
		// Legacy workers have no batch naming metadata. A partially present new
		// schema must never silently fall back to the shared AWS Name tag.
		_, basePresent := tags["BaseName"]
		_, attemptPresent := tags["AttemptId"]
		_, batchPresent := tags["BatchId"]
		return !basePresent && !attemptPresent && !batchPresent
	}
	if version != "1" || !ValidName(tags["BaseName"]) || tags["Name"] != tags["BaseName"] ||
		!ValidRequest(tags["RequestId"]) || tags["BatchId"] != tags["RequestId"] || !ValidRequest(tags["AttemptId"]) ||
		tags["Profile"] != "agent" || tags["CreatedAt"] == "" {
		return false
	}
	if i.State != nil {
		switch i.State.Name {
		case "", types.InstanceStateNamePending, types.InstanceStateNameRunning, types.InstanceStateNameStopping, types.InstanceStateNameStopped, types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated:
		default:
			return false
		}
	}
	// These are observations of actual placement, not the launch's eligible
	// choices. Terminated instances may no longer expose current placement.
	terminal := i.State != nil && (i.State.Name == types.InstanceStateNameTerminated || i.State.Name == types.InstanceStateNameShuttingDown)
	validResource := func(value, prefix string) bool {
		return strings.HasPrefix(value, prefix+"-") && fleetResourceIDRE.MatchString(value)
	}
	if (!terminal || i.ImageId != nil) && !validResource(aws.ToString(i.ImageId), "ami") {
		return false
	}
	if (!terminal || i.InstanceType != "") && !fleetTypeRE.MatchString(string(i.InstanceType)) {
		return false
	}
	if (!terminal || i.SubnetId != nil) && !validResource(aws.ToString(i.SubnetId), "subnet") {
		return false
	}
	if !terminal && (i.Placement == nil || i.Placement.AvailabilityZone == nil) {
		return false
	}
	if i.Placement != nil && i.Placement.AvailabilityZone != nil && !validInventoryZone(aws.ToString(i.Placement.AvailabilityZone)) {
		return false
	}
	versionNumber, err := strconv.ParseInt(tags["aws:ec2launchtemplate:version"], 10, 64)
	return validResource(tags["aws:ec2launchtemplate:id"], "lt") && err == nil && versionNumber > 0 &&
		strconv.FormatInt(versionNumber, 10) == tags["aws:ec2launchtemplate:version"] &&
		(i.InstanceLifecycle == "" || i.InstanceLifecycle == types.InstanceLifecycleTypeSpot)
}

func validInventoryZone(zone string) bool {
	return len(zone) == len("us-east-2a") && strings.HasPrefix(zone, "us-east-2") && zone[len(zone)-1] >= 'a' && zone[len(zone)-1] <= 'z'
}

func generatedNameCandidate(name string) bool {
	separator := strings.LastIndex(name, "-i-")
	return separator > 0 && instanceRE.MatchString(name[separator+1:])
}

// Changes in state between pages are normal; changes in identity or placement
// for the same ID are contradictory. Preserve all volume IDs in either case.
func sameInventoryIdentity(a, b Instance) bool {
	a.State, b.State = "", ""
	a.RootDeletion, b.RootDeletion = "", ""
	a.Volumes, b.Volumes = nil, nil
	return reflect.DeepEqual(a, b)
}

func (s *Service) inventory(ctx context.Context, id, name, request string) ([]Instance, error) {
	return s.inventorySelection(ctx, id, name, request, "")
}

func (s *Service) inventorySelection(ctx context.Context, id, name, request, group string) (found []Instance, resultErr error) {
	found = []Instance{}
	now := clockNow(s.Clock)
	defer func() { sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID }) }()
	in := &ec2.DescribeInstancesInput{}
	// A generated public name differs from AWS Name. Scan the complete scope so
	// an identically named legacy worker still participates in ambiguity checks.
	cloudName := name
	if generatedNameCandidate(name) {
		cloudName = ""
	}
	if id != "" {
		in.InstanceIds = []string{id}
	} else {
		in.Filters = []types.Filter{{Name: aws.String("tag:ManagedBy"), Values: []string{"devbox"}}, {Name: aws.String("tag:Deployment"), Values: []string{s.Scope.Deployment}}, {Name: aws.String("tag:Owner"), Values: []string{s.Scope.Owner}}}
		if cloudName != "" {
			in.Filters = append(in.Filters, types.Filter{Name: aws.String("tag:Name"), Values: []string{cloudName}})
		}
		if request != "" {
			in.Filters = append(in.Filters, types.Filter{Name: aws.String("tag:RequestId"), Values: []string{request}})
		}
		if group != "" {
			in.Filters = append(in.Filters, types.Filter{Name: aws.String("tag:Group"), Values: []string{group}})
		}
	}
	seen := map[string]int{}
	observed := map[string]Instance{}
	tokens := map[string]bool{}
	invalid := func(code, message string) {
		if resultErr == nil {
			resultErr = failure(code, message)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return found, err
		}
		out, err := s.API.DescribeInstances(ctx, in)
		if id != "" && apiCode(err, "InvalidInstanceID.NotFound") && out == nil && len(found) == 0 && resultErr == nil && in.NextToken == nil {
			return found, nil
		}
		if out != nil {
			for _, res := range out.Reservations {
				for _, i := range res.Instances {
					tags, tagOK := inventoryTags(i)
					if !tagOK {
						invalid("inventory_invalid", "AWS inventory contained malformed or duplicate tags; retry inspection before mutation")
						continue
					}
					if aws.ToString(res.OwnerId) != s.Scope.ExpectedAccount || tags["ManagedBy"] != "devbox" || tags["Deployment"] != s.Scope.Deployment || tags["Owner"] != s.Scope.Owner {
						invalid("scope_mismatch", "AWS returned an instance outside the expected account/deployment/owner; no mutation performed")
						continue
					}
					r := record(i)
					inspectInstanceExpiry(&r, i.Tags, now)
					if !validInventoryIdentity(i, tags) || (id != "" && r.ID != id) || (cloudName != "" && tags["Name"] != cloudName) || (request != "" && r.RequestID != request) || (group != "" && r.Group != group) {
						invalid("inventory_invalid", "AWS inventory did not match the requested identity or naming schema; retry inspection before mutation")
						continue
					}
					if prior, exists := observed[r.ID]; exists {
						if !sameInventoryIdentity(prior, r) {
							if n, selected := seen[r.ID]; selected {
								found[n].ObservationCode = "inventory_invalid"
							}
							invalid("inventory_invalid", "AWS inventory returned conflicting observations for one identity; retry inspection before mutation")
						}
					} else {
						observed[r.ID] = r
					}
					if name != "" && r.Name != name {
						continue
					}
					if n, exists := seen[r.ID]; exists {
						found[n].Volumes = mergeFleetVolumes(found[n].Volumes, r.Volumes)
						continue
					}
					seen[r.ID] = len(found)
					found = append(found, r)
				}
			}
		}
		if err != nil || out == nil {
			if ctx.Err() != nil {
				return found, ctx.Err()
			}
			return found, failure("inventory_unavailable", "cannot read EC2 inventory; check credentials, regional DescribeInstances permission and connectivity, then retry")
		}
		next := aws.ToString(out.NextToken)
		if next == "" {
			break
		}
		if tokens[next] {
			return found, failure("inventory_invalid", "EC2 inventory pagination repeated; retry before mutation")
		}
		tokens[next] = true
		in.NextToken = out.NextToken
	}
	return found, resultErr
}
func (s *Service) List(ctx context.Context) ([]Instance, error) { return s.inventory(ctx, "", "", "") }
func (s *Service) ListGroup(ctx context.Context, group string) ([]Instance, error) {
	if !ValidGroup(group) {
		return []Instance{}, failure("group_invalid", "use a valid group name")
	}
	return s.inventorySelection(ctx, "", "", "", group)
}
func active(instances []Instance) []Instance {
	result := []Instance{}
	for _, i := range instances {
		if i.State != "terminated" {
			result = append(result, i)
		}
	}
	return result
}
