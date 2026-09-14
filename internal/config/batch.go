package config

import (
	"errors"
	"regexp"
)

const (
	DefaultMaxCount = 10
	HardMaxCount    = 100
)

// Subnet pins the foundation's subnet-to-AZ mapping. Offering information is
// capability evidence only; it does not promise current Spot capacity.
type Subnet struct {
	ID               string `json:"id"`
	AvailabilityZone string `json:"availability_zone"`
}

// CompatiblePool enumerates the subnets where this architecture-compatible
// instance type is offered. A type appears exactly once in a manifest.
type CompatiblePool struct {
	InstanceType string   `json:"instance_type"`
	Architecture string   `json:"architecture"`
	SubnetIDs    []string `json:"subnet_ids"`
}

// RootDisk is the effective root mapping of the pinned launch-template version.
// The resolved launch plan can increase or decrease SizeGB down to the pinned
// AMI snapshot minimum by supplying a CreateFleet override.
type RootDisk struct {
	SizeGB              int    `json:"size_gb"`
	Type                string `json:"type"`
	Encrypted           bool   `json:"encrypted"`
	DeleteOnTermination bool   `json:"delete_on_termination"`
}

// LaunchChoice is one foundation-approved type/subnet combination remaining
// after applying the profile's restrictions. The order follows profile types
// and each foundation pool's subnet order, never a cloud response order.
type LaunchChoice struct {
	InstanceType     string `json:"instance_type"`
	SubnetID         string `json:"subnet_id"`
	AvailabilityZone string `json:"availability_zone"`
}

// LaunchLedger pins shared immutable launch records in the results bucket.
// Launch records have no automatic expiry and survive result-log expiration.
type LaunchLedger struct {
	SchemaVersion       int    `json:"schema_version"`
	Bucket              string `json:"bucket"`
	ExpectedBucketOwner string `json:"expected_bucket_owner"`
	Region              string `json:"region"`
	Prefix              string `json:"prefix"`
	PolicySHA256        string `json:"policy_sha256"`
}

func LaunchLedgerPrefix(c Config) string {
	return "launches/v2/" + c.ExpectedAccount + "/" + c.Region + "/" + c.Deployment + "/" + c.Owner + "/"
}

func ValidateMaxCount(maximum int) error {
	if maximum < 1 || maximum > HardMaxCount {
		return errors.New("max_count must be an integer between 1 and 100; omitted max_count defaults to 10")
	}
	return nil
}

var availabilityZoneRE = regexp.MustCompile(`^us-east-2[a-z]$`)

// NormalizeProfile preserves profile-v1 behavior with explicit supported
// defaults. LoadProfile separately rejects v2-only fields in v1 TOML, including
// fields supplied with empty or false values.
func NormalizeProfile(p Profile) (Profile, error) {
	if p.SchemaVersion != 1 && p.SchemaVersion != 2 {
		return p, errors.New("unsupported profile schema_version; use version 1 or 2")
	}
	if p.SchemaVersion == 1 {
		if len(p.SubnetIDs) != 0 || len(p.AvailabilityZones) != 0 {
			return p, errors.New("profile subnet and availability-zone restrictions require version 2")
		}
		if p.Architecture == "" {
			p.Architecture = "x86_64"
		}
		if p.DiskType == "" {
			p.DiskType = "gp3"
		}
		p.Encrypted = true
		p.DeleteOnTermination = true
	}
	if p.Name != "agent" || p.Image != "agent" {
		return p, errors.New("profile name and image must be agent")
	}
	if p.Market != "spot" && p.Market != "on-demand" {
		return p, errors.New("profile market must be spot or on-demand")
	}
	if len(p.InstanceTypes) == 0 {
		return p, errors.New("profile instance_types must contain at least one instance type")
	}
	seen := map[string]bool{}
	for _, instanceType := range p.InstanceTypes {
		if !typeRE.MatchString(instanceType) || seen[instanceType] {
			return p, errors.New("profile instance_types must be valid, distinct EC2 type names")
		}
		seen[instanceType] = true
	}
	if p.Architecture != "x86_64" {
		return p, errors.New("profile architecture must be x86_64; the foundation supports Ubuntu 24.04 amd64 only")
	}
	if err := validateRootDisk(RootDisk{SizeGB: p.DiskGB, Type: p.DiskType, Encrypted: p.Encrypted, DeleteOnTermination: p.DeleteOnTermination}); err != nil {
		return p, err
	}
	seen = map[string]bool{}
	for _, id := range p.SubnetIDs {
		if !resourceID(id, "subnet") || seen[id] {
			return p, errors.New("profile subnet_ids must contain valid, distinct subnet IDs")
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, zone := range p.AvailabilityZones {
		if !availabilityZoneRE.MatchString(zone) || seen[zone] {
			return p, errors.New("profile availability_zones must contain valid, distinct Ohio availability zones")
		}
		seen[zone] = true
	}
	return p, nil
}

func ValidateProfile(p Profile) error {
	_, err := NormalizeProfile(p)
	return err
}

func validateRootDisk(d RootDisk) error {
	if d.SizeGB < 8 || d.SizeGB > 16384 || d.Type != "gp3" || !d.Encrypted || !d.DeleteOnTermination {
		return errors.New("root disk must be 8–16384 GiB, gp3, encrypted and deleted on termination")
	}
	return nil
}

func validateManifestV5(m Manifest) error {
	c := Config{ExpectedAccount: m.Account, Region: m.Region, Deployment: m.Deployment, Owner: m.Owner}
	ledger := m.LaunchLedger
	if ledger == nil || ledger.SchemaVersion != 1 || ledger.Bucket != m.Results.Bucket || ledger.ExpectedBucketOwner != m.Account || ledger.Region != m.Region || ledger.Prefix != LaunchLedgerPrefix(c) || ledger.PolicySHA256 != m.Results.PolicySHA256 {
		return errors.New("manifest version 5 requires scoped launch ledger configuration in the results bucket with its policy digest")
	}
	if len(m.Subnets) == 0 || len(m.Subnets) != len(m.SubnetIDs) {
		return errors.New("manifest version 5 requires an exact subnet-to-availability-zone mapping")
	}
	ids := map[string]bool{}
	for _, id := range m.SubnetIDs {
		if !resourceID(id, "subnet") || ids[id] {
			return errors.New("manifest subnet_ids must contain valid, distinct subnet IDs")
		}
		ids[id] = true
	}
	subnets := map[string]Subnet{}
	seenZones := map[string]bool{}
	for _, subnet := range m.Subnets {
		if !ids[subnet.ID] || !availabilityZoneRE.MatchString(subnet.AvailabilityZone) {
			return errors.New("manifest subnets must map every permitted subnet to an Ohio availability zone")
		}
		if _, exists := subnets[subnet.ID]; exists {
			return errors.New("manifest subnets must contain distinct subnet IDs")
		}
		if seenZones[subnet.AvailabilityZone] {
			return errors.New("instant Fleet permits only one selected subnet per availability zone")
		}
		seenZones[subnet.AvailabilityZone] = true
		subnets[subnet.ID] = subnet
	}
	if len(m.CompatiblePools) == 0 {
		return errors.New("manifest version 5 requires compatible instance-type/subnet pools")
	}
	seenTypes := map[string]bool{}
	for _, pool := range m.CompatiblePools {
		if !typeRE.MatchString(pool.InstanceType) || seenTypes[pool.InstanceType] || pool.Architecture != "x86_64" || len(pool.SubnetIDs) == 0 {
			return errors.New("manifest compatible pools require distinct instance types, x86_64 architecture and permitted subnets")
		}
		seenTypes[pool.InstanceType] = true
		seenSubnets := map[string]bool{}
		for _, id := range pool.SubnetIDs {
			if !ids[id] || seenSubnets[id] {
				return errors.New("manifest compatible pool subnets must be distinct members of subnet_ids")
			}
			seenSubnets[id] = true
		}
	}
	for _, img := range m.Images {
		if img.RootDisk == nil || img.MinimumRootDiskGB < 1 || img.MinimumRootDiskGB > 16384 {
			return errors.New("manifest version 5 requires effective template root disk settings and the AMI snapshot minimum size")
		}
		if err := validateRootDisk(*img.RootDisk); err != nil {
			return err
		}
		if img.RootDisk.SizeGB < img.MinimumRootDiskGB {
			return errors.New("template root disk is smaller than the AMI snapshot minimum")
		}
	}
	return nil
}

// ValidateProfileManifest is pure validation of the complete manifest, profile
// and their compatible choices. It requires version 5 for a batch launch. It
// does not contact AWS: later foundation verification must confirm these pins.
// The caller must also match the manifest scope to the selected configuration.
func ValidateProfileManifest(p Profile, m Manifest) ([]LaunchChoice, error) {
	p, err := NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if m.SchemaVersion != 5 {
		return nil, errors.New("batch launch requires manifest version 5; apply and re-export the foundation")
	}
	c := Config{ExpectedAccount: m.Account, Region: m.Region, Deployment: m.Deployment, Owner: m.Owner}
	if _, err := validateManifest(m, c, Profile{Image: "agent"}); err != nil {
		return nil, err
	}
	img := m.Images[p.Image]
	if p.Architecture != img.Architecture || p.DiskGB < img.MinimumRootDiskGB {
		return nil, errors.New("profile architecture or root disk size conflicts with the pinned AMI")
	}
	subnets := map[string]Subnet{}
	zones := map[string]bool{}
	for _, subnet := range m.Subnets {
		subnets[subnet.ID] = subnet
		zones[subnet.AvailabilityZone] = true
	}
	selectedSubnets := map[string]bool{}
	for _, id := range p.SubnetIDs {
		if _, exists := subnets[id]; !exists {
			return nil, errors.New("profile subnet_ids contains a subnet outside the foundation")
		}
		selectedSubnets[id] = true
	}
	selectedZones := map[string]bool{}
	for _, zone := range p.AvailabilityZones {
		if !zones[zone] {
			return nil, errors.New("profile availability_zones contains a zone outside the foundation")
		}
		selectedZones[zone] = true
	}
	pools := map[string]CompatiblePool{}
	for _, pool := range m.CompatiblePools {
		pools[pool.InstanceType] = pool
	}
	choices := []LaunchChoice{}
	for _, instanceType := range p.InstanceTypes {
		pool, exists := pools[instanceType]
		if !exists || pool.Architecture != p.Architecture {
			return nil, errors.New("profile instance type is not compatible with the foundation image")
		}
		before := len(choices)
		for _, id := range pool.SubnetIDs {
			subnet := subnets[id]
			if len(selectedSubnets) != 0 && !selectedSubnets[id] || len(selectedZones) != 0 && !selectedZones[subnet.AvailabilityZone] {
				continue
			}
			choices = append(choices, LaunchChoice{InstanceType: instanceType, SubnetID: id, AvailabilityZone: subnet.AvailabilityZone})
		}
		if len(choices) == before {
			return nil, errors.New("every profile instance type must have an offering within the selected subnets and availability zones")
		}
	}
	return choices, nil
}
