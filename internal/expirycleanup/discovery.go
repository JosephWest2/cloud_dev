package expirycleanup

import (
	"context"
	"reflect"
	"regexp"
	"sort"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

var instanceID = regexp.MustCompile(`^i-([0-9a-f]{8}|[0-9a-f]{17})$`)
var volumeID = regexp.MustCompile(`^vol-([0-9a-f]{8}|[0-9a-f]{17})$`)
var zoneSuffix = regexp.MustCompile(`^([a-z]|-[a-z0-9]+(-[a-z0-9]+)*-[0-9]+[a-z])$`)

type record struct {
	resource   expiry.Resource
	rootDevice string
	rootType   string
	volumes    []expiry.Volume
	badMapping bool
	conflict   bool
}

func copyString(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func (s *Service) record(owner *string, i types.Instance) record {
	r := record{resource: expiry.Resource{ID: aws.ToString(i.InstanceId), Account: aws.ToString(owner), Region: s.scope.Region, State: "unknown", Tags: []expiry.Tag{}}, rootDevice: aws.ToString(i.RootDeviceName), rootType: string(i.RootDeviceType), volumes: []expiry.Volume{}}
	if !instanceID.MatchString(r.resource.ID) {
		r.resource.ID = ""
	}
	if i.State != nil {
		switch i.State.Name {
		case "pending", "running", "stopping", "stopped", "shutting-down", "terminated":
			r.resource.State = string(i.State.Name)
		}
	}
	if i.State != nil && i.State.Code != nil {
		expected := map[string]int32{"pending": 0, "running": 16, "shutting-down": 32, "terminated": 48, "stopping": 64, "stopped": 80}
		if code, ok := expected[r.resource.State]; !ok || (*i.State.Code&255) != code {
			r.resource.State = "unknown"
		}
	}
	// The regional client supplies region evidence; placement, when present,
	// must not contradict it. Terminal EC2 records may omit placement entirely.
	if i.Placement != nil && i.Placement.AvailabilityZone != nil && !s.validZone(*i.Placement.AvailabilityZone) {
		r.resource.Region = ""
	}
	for _, t := range i.Tags {
		r.resource.Tags = append(r.resource.Tags, expiry.Tag{Key: copyString(t.Key), Value: copyString(t.Value)})
	}
	sort.SliceStable(r.resource.Tags, func(i, j int) bool {
		a, b := r.resource.Tags[i], r.resource.Tags[j]
		if aws.ToString(a.Key) != aws.ToString(b.Key) {
			return aws.ToString(a.Key) < aws.ToString(b.Key)
		}
		return aws.ToString(a.Value) < aws.ToString(b.Value)
	})
	devices, ids := map[string]bool{}, map[string]bool{}
	for _, b := range i.BlockDeviceMappings {
		device := aws.ToString(b.DeviceName)
		if b.Ebs == nil {
			r.badMapping = true
			continue
		}
		id := aws.ToString(b.Ebs.VolumeId)
		if !volumeID.MatchString(id) {
			r.badMapping = true
			continue
		}
		v := expiry.Volume{ID: id, Device: device, Root: device != "" && device == r.rootDevice, Deletion: "not_observed"}
		if b.Ebs.DeleteOnTermination != nil {
			flag := *b.Ebs.DeleteOnTermination
			v.DeleteOnTermination = &flag
			if !flag {
				v.Deletion = "retained"
			}
		} else {
			v.Deletion = "unavailable"
		}
		if device == "" || devices[device] || ids[id] {
			r.badMapping = true
		}
		devices[device] = true
		ids[id] = true
		r.volumes = append(r.volumes, v)
	}
	sort.SliceStable(r.volumes, func(i, j int) bool {
		a, b := r.volumes[i], r.volumes[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Device < b.Device
	})
	return r
}
func (s *Service) filters() []types.Filter {
	return []types.Filter{
		{Name: aws.String("tag:ManagedBy"), Values: []string{"devbox"}},
		{Name: aws.String("tag:Deployment"), Values: []string{s.scope.Deployment}},
		{Name: aws.String("tag:Owner"), Values: []string{s.scope.Owner}},
	}
}

// scan preserves observations even from an output returned alongside an error.
// Exact-ID reads do not coalesce duplicates: complete singularity is required.
func (s *Service) scan(ctx context.Context, id string) ([]record, bool) {
	records := []record{}
	seenIDs := map[string]int{}
	tokens := map[string]bool{}
	token := ""
	for page := 0; page < s.limits.MaxPages; page++ {
		if ctx.Err() != nil {
			return records, false
		}
		in := &ec2.DescribeInstancesInput{Filters: s.filters()}
		if id != "" {
			in.InstanceIds = []string{id}
		} else {
			in.MaxResults = aws.Int32(1000)
		}
		if token != "" {
			in.NextToken = aws.String(token)
		}
		request, cancel := context.WithTimeout(ctx, s.limits.RequestTimeout)
		out, err := s.deps.EC2.DescribeInstances(request, in, s.reads)
		requestErr := request.Err()
		cancel()
		complete := err == nil && requestErr == nil && out != nil
		if out != nil {
			for _, reservation := range out.Reservations {
				if len(reservation.Instances) == 0 {
					complete = false
				}
				for _, instance := range reservation.Instances {
					rec := s.record(reservation.OwnerId, instance)
					if id == "" && rec.resource.ID != "" {
						if index, ok := seenIDs[rec.resource.ID]; ok {
							old := &records[index]
							if !reflect.DeepEqual(*old, rec) {
								old.conflict = true
								old.volumes = mergeMappings(old.volumes, rec.volumes)
							}
							continue
						}
						seenIDs[rec.resource.ID] = len(records)
					}
					records = append(records, rec)
				}
			}
		}
		if !complete {
			return records, false
		}
		token = aws.ToString(out.NextToken)
		if token == "" {
			return records, true
		}
		if tokens[token] {
			return records, false
		}
		tokens[token] = true
	}
	return records, false
}
func (s *Service) exact(ctx context.Context, id string) (record, bool) {
	rs, complete := s.scan(ctx, id)
	if !complete || len(rs) != 1 || rs[0].resource.ID != id {
		return record{}, false
	}
	return rs[0], true
}

// Historical absence is benign only after policy verifies an already-terminal
// row. Discarded malformed mappings and explicit incompatible root types are
// errors, not absence. This exception never authorizes deletion observation.
func historicalMappingsAbsent(r record, reason expiry.Reason) bool {
	return reason == expiry.AlreadyTerminated && !r.conflict && !r.badMapping &&
		len(r.volumes) == 0 && (r.rootType == "" || r.rootType == "ebs")
}

func rootProblem(r record) string {
	if r.badMapping || r.rootType != "ebs" || r.rootDevice == "" {
		return "root_volume_unverified"
	}
	roots := 0
	var flag *bool
	for _, v := range r.volumes {
		if v.Root {
			roots++
			flag = v.DeleteOnTermination
		}
	}
	if roots != 1 || flag == nil {
		return "root_volume_unverified"
	}
	if !*flag {
		return "root_volume_retained"
	}
	return ""
}
func sameMappings(a, b record) bool {
	return a.rootDevice == b.rootDevice && a.rootType == b.rootType && a.badMapping == b.badMapping && reflect.DeepEqual(a.volumes, b.volumes)
}
func mergeMappings(a, b []expiry.Volume) []expiry.Volume {
	out := cloneVolumes(a)
	for _, v := range b {
		found := false
		for _, old := range out {
			if reflect.DeepEqual(v, old) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, cloneVolumes([]expiry.Volume{v})[0])
		}
	}
	return out
}
