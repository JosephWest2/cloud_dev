package lifecycle

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"sort"
)

type Volume struct {
	ID                  string `json:"volume_id"`
	Device              string `json:"device"`
	Root                bool   `json:"root"`
	DeleteOnTermination bool   `json:"delete_on_termination"`
	Deletion            string `json:"deletion"`
}
type Instance struct {
	ID              string   `json:"instance_id"`
	Name            string   `json:"name"`
	RequestID       string   `json:"request_id"`
	Profile         string   `json:"profile"`
	CreatedAt       string   `json:"created_at"`
	Image           string   `json:"image_id"`
	Type            string   `json:"instance_type"`
	Market          string   `json:"market"`
	TemplateID      string   `json:"launch_template_id"`
	TemplateVersion string   `json:"launch_template_version"`
	State           string   `json:"ec2_state"`
	SSM             string   `json:"ssm"`
	Bootstrap       string   `json:"bootstrap"`
	Readiness       string   `json:"readiness"`
	RootDeletion    string   `json:"root_volume_deletion"`
	Volumes         []Volume `json:"volumes"`
	ObservationCode string   `json:"observation_code,omitempty"`
	ProbeCommandID  string   `json:"probe_command_id,omitempty"`
	HostKey         string   `json:"-"`
	clientToken     string
}

func tagsOf(i types.Instance) map[string]string {
	tags := map[string]string{}
	for _, t := range i.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return tags
}
func (s *Service) scoped(i types.Instance) bool {
	t := tagsOf(i)
	return t["ManagedBy"] == "devbox" && t["Deployment"] == s.Scope.Deployment && t["Owner"] == s.Scope.Owner
}
func record(i types.Instance) Instance {
	t := tagsOf(i)
	r := Instance{ID: aws.ToString(i.InstanceId), Name: t["Name"], RequestID: t["RequestId"], Profile: t["Profile"], CreatedAt: t["CreatedAt"], Image: aws.ToString(i.ImageId), Type: string(i.InstanceType), Market: "on-demand", TemplateID: t["aws:ec2launchtemplate:id"], TemplateVersion: t["aws:ec2launchtemplate:version"], State: "unknown", SSM: "not_observed", Bootstrap: "not_observed", Readiness: "not_observed", RootDeletion: "unavailable", Volumes: []Volume{}, clientToken: aws.ToString(i.ClientToken)}
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
func (s *Service) inventory(ctx context.Context, id, name, request string) ([]Instance, error) {
	in := &ec2.DescribeInstancesInput{}
	if id != "" {
		in.InstanceIds = []string{id}
	} else {
		in.Filters = []types.Filter{{Name: aws.String("tag:ManagedBy"), Values: []string{"devbox"}}, {Name: aws.String("tag:Deployment"), Values: []string{s.Scope.Deployment}}, {Name: aws.String("tag:Owner"), Values: []string{s.Scope.Owner}}}
		if name != "" {
			in.Filters = append(in.Filters, types.Filter{Name: aws.String("tag:Name"), Values: []string{name}})
		}
		if request != "" {
			in.Filters = append(in.Filters, types.Filter{Name: aws.String("tag:RequestId"), Values: []string{request}})
		}
	}
	found := []Instance{}
	seen := map[string]bool{}
	tokens := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return found, err
		}
		out, err := s.API.DescribeInstances(ctx, in)
		if id != "" && apiCode(err, "InvalidInstanceID.NotFound") {
			return found, nil
		}
		if err != nil || out == nil {
			return found, failure("inventory_unavailable", "cannot read EC2 inventory; check credentials, regional DescribeInstances permission and connectivity, then retry")
		}
		for _, res := range out.Reservations {
			for _, i := range res.Instances {
				if aws.ToString(res.OwnerId) != s.Scope.ExpectedAccount || !s.scoped(i) {
					return found, failure("scope_mismatch", "AWS returned an instance outside the expected account/deployment/owner; no mutation performed")
				}
				r := record(i)
				if !instanceRE.MatchString(r.ID) || (id != "" && r.ID != id) || (name != "" && r.Name != name) || (request != "" && r.RequestID != request) {
					return found, failure("inventory_invalid", "AWS inventory did not match the requested identity; retry inspection before mutation")
				}
				if !seen[r.ID] {
					found = append(found, r)
					seen[r.ID] = true
				}
			}
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
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found, nil
}
func (s *Service) List(ctx context.Context) ([]Instance, error) { return s.inventory(ctx, "", "", "") }
func active(instances []Instance) []Instance {
	result := []Instance{}
	for _, i := range instances {
		if i.State != "terminated" {
			result = append(result, i)
		}
	}
	return result
}
