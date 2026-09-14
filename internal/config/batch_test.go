package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestMaximumCountConfiguration(t *testing.T) {
	// Environment variables do not bypass the configured request cap.
	t.Setenv("DEVBOX_MAX_COUNT", "99")
	t.Setenv("MAX_COUNT", "99")
	for _, tc := range []struct {
		name, value string
		want        int
	}{
		{"omitted", "", DefaultMaxCount},
		{"minimum", "1", 1},
		{"configured", "4", 4},
		{"maximum", "100", HardMaxCount},
		{"zero", "0", 0},
		{"negative", "-1", 0},
		{"excessive", "101", 0},
		{"fractional", "1.5", 0},
		{"string", "'2'", 0},
		{"overflow", "99999999999999999999999", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := testutil.Config
			if tc.value != "" {
				data += "max_count = " + tc.value + "\n"
			}
			path := testutil.Write(t, filepath.Join(t.TempDir(), "config.toml"), data)
			c, err := Load(path, Overrides{})
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid maximum accepted")
				}
				return
			}
			if err != nil || c.MaxCount != tc.want {
				t.Fatalf("max_count = %d, want %d: %v", c.MaxCount, tc.want, err)
			}
		})
	}
}

const profileV1 = "schema_version=1\nname='agent'\nmarket='spot'\ninstance_types=['c7i.2xlarge']\nimage='agent'\ndisk_gb=100\n"
const profileV2 = "schema_version=2\nname='agent'\nmarket='spot'\ninstance_types=['c7i.2xlarge']\nimage='agent'\ndisk_gb=100\narchitecture='x86_64'\ndisk_type='gp3'\nencrypted=true\ndelete_on_termination=true\n"

func TestVersionedProfileDefaultsAndOptions(t *testing.T) {
	for _, tc := range []struct {
		name, data string
	}{
		{"legacy defaults", profileV1},
		{"explicit v2", profileV2},
		{"empty restrictions", profileV2 + "subnet_ids=[]\navailability_zones=[]\n"},
		{"restrictions", profileV2 + "subnet_ids=['subnet-12345678']\navailability_zones=['us-east-2a']\n"},
		{"on demand profile requires separate launch opt in", strings.Replace(profileV2, "market='spot'", "market='on-demand'", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), tc.data))
			if err != nil {
				t.Fatal(err)
			}
			if p.Architecture != "x86_64" || p.DiskType != "gp3" || !p.Encrypted || !p.DeleteOnTermination {
				t.Fatalf("missing supported profile defaults: %+v", p)
			}
			if err := ValidateProfile(p); err != nil {
				t.Fatal(err)
			}
		})
	}
	p, err := LoadProfile("")
	if err != nil || p.SchemaVersion != 2 || p.Market != "spot" || p.Architecture != "x86_64" || !p.Encrypted || !p.DeleteOnTermination {
		t.Fatalf("bundled v2 Spot defaults: %+v, %v", p, err)
	}
}

func TestProfileV1RejectsV2FieldPresence(t *testing.T) {
	for _, field := range []string{
		"architecture='x86_64'", "architecture=''", "disk_type='gp3'", "disk_type=''",
		"encrypted=true", "encrypted=false", "delete_on_termination=true", "delete_on_termination=false",
		"subnet_ids=[]", "availability_zones=[]",
	} {
		t.Run(field, func(t *testing.T) {
			_, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), profileV1+field+"\n"))
			if err == nil {
				t.Fatal("version 2 option accepted under version 1")
			}
		})
	}
}

func TestLegacyManifestRejectsProfilePlacementRestrictions(t *testing.T) {
	c, err := Load(testutil.Setup(t), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	for _, restriction := range []string{"subnet_ids=['subnet-87654321']\n", "availability_zones=['us-east-2b']\n"} {
		p, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), profileV2+restriction))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(c.Manifest, c, p); err == nil {
			t.Fatal("legacy allocator could ignore profile placement restrictions")
		}
	}
}

func TestProfileV2RejectsInvalidOrMissingOptions(t *testing.T) {
	for _, tc := range []struct {
		name, data string
	}{
		{"unsupported schema", strings.Replace(profileV2, "schema_version=2", "schema_version=3", 1)},
		{"missing architecture", strings.Replace(profileV2, "architecture='x86_64'\n", "", 1)},
		{"unsupported architecture", strings.Replace(profileV2, "x86_64", "arm64", 1)},
		{"missing disk type", strings.Replace(profileV2, "disk_type='gp3'\n", "", 1)},
		{"unsupported disk type", strings.Replace(profileV2, "gp3", "gp2", 1)},
		{"missing encryption", strings.Replace(profileV2, "encrypted=true\n", "", 1)},
		{"unencrypted root", strings.Replace(profileV2, "encrypted=true", "encrypted=false", 1)},
		{"missing disposable setting", strings.Replace(profileV2, "delete_on_termination=true\n", "", 1)},
		{"persistent root", strings.Replace(profileV2, "delete_on_termination=true", "delete_on_termination=false", 1)},
		{"wrong image", strings.Replace(profileV2, "image='agent'", "image='other'", 1)},
		{"duplicate type", strings.Replace(profileV2, "['c7i.2xlarge']", "['c7i.2xlarge','c7i.2xlarge']", 1)},
		{"invalid subnet", profileV2 + "subnet_ids=['SECRET']\n"},
		{"duplicate subnet", profileV2 + "subnet_ids=['subnet-12345678','subnet-12345678']\n"},
		{"unsupported region zone", profileV2 + "availability_zones=['us-west-2a']\n"},
		{"invalid zone", profileV2 + "availability_zones=['us-east-2']\n"},
		{"local zone", profileV2 + "availability_zones=['us-east-2-cmh-1a']\n"},
		{"duplicate zone", profileV2 + "availability_zones=['us-east-2a','us-east-2a']\n"},
		{"too small root", strings.Replace(profileV2, "disk_gb=100", "disk_gb=7", 1)},
		{"too large root", strings.Replace(profileV2, "disk_gb=100", "disk_gb=16385", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), tc.data))
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("expected redacted rejection: %v", err)
			}
		})
	}
}

func manifestV5(t *testing.T) (Config, Manifest, Profile) {
	t.Helper()
	c, err := Load(testutil.Setup(t), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion = 5
	m.SubnetIDs = []string{"subnet-12345678", "subnet-87654321"}
	m.Subnets = []Subnet{{ID: m.SubnetIDs[0], AvailabilityZone: "us-east-2a"}, {ID: m.SubnetIDs[1], AvailabilityZone: "us-east-2b"}}
	m.CompatiblePools = []CompatiblePool{
		{InstanceType: "c7i.2xlarge", Architecture: "x86_64", SubnetIDs: []string{m.SubnetIDs[0], m.SubnetIDs[1]}},
		{InstanceType: "c7a.2xlarge", Architecture: "x86_64", SubnetIDs: []string{m.SubnetIDs[1]}},
	}
	img := m.Images["agent"]
	img.RootDisk = &RootDisk{SizeGB: 100, Type: "gp3", Encrypted: true, DeleteOnTermination: true}
	img.MinimumRootDiskGB = 8
	m.Images["agent"] = img
	m.LaunchLedger = &LaunchLedger{SchemaVersion: 1, Bucket: m.Results.Bucket, ExpectedBucketOwner: m.Account, Region: m.Region, Prefix: LaunchLedgerPrefix(c), PolicySHA256: m.Results.PolicySHA256}
	p, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), profileV2))
	if err != nil {
		t.Fatal(err)
	}
	p.InstanceTypes = []string{"c7i.2xlarge", "c7a.2xlarge"}
	return c, m, p
}

func writeManifest(t *testing.T, c Config, m Manifest) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, c.Manifest, string(data))
}

func TestManifestV5CompatibleChoices(t *testing.T) {
	c, m, p := manifestV5(t)
	writeManifest(t, c, m)
	if _, err := LoadManifest(c.Manifest, c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExecutionManifest(c.Manifest, c); err != nil {
		t.Fatal(err)
	}
	choices, err := ValidateProfileManifest(p, m)
	want := []LaunchChoice{
		{InstanceType: "c7i.2xlarge", SubnetID: "subnet-12345678", AvailabilityZone: "us-east-2a"},
		{InstanceType: "c7i.2xlarge", SubnetID: "subnet-87654321", AvailabilityZone: "us-east-2b"},
		{InstanceType: "c7a.2xlarge", SubnetID: "subnet-87654321", AvailabilityZone: "us-east-2b"},
	}
	if err != nil || !reflect.DeepEqual(choices, want) {
		t.Fatalf("choices = %+v, want %+v: %v", choices, want, err)
	}
	for _, restricted := range []Profile{
		func() Profile { q := p; q.SubnetIDs = []string{"subnet-87654321"}; return q }(),
		func() Profile { q := p; q.AvailabilityZones = []string{"us-east-2b"}; return q }(),
		func() Profile {
			q := p
			q.SubnetIDs = []string{"subnet-87654321"}
			q.AvailabilityZones = []string{"us-east-2b"}
			return q
		}(),
	} {
		choices, err := ValidateProfileManifest(restricted, m)
		if err != nil || !reflect.DeepEqual(choices, want[1:]) {
			t.Fatalf("restricted choices = %+v, want %+v: %v", choices, want[1:], err)
		}
	}
	// Fleet root-disk overrides may be smaller or larger than the template
	// default, provided that they satisfy the AMI snapshot minimum.
	for _, size := range []int{8, 50, 150, 16384} {
		p.DiskGB = size
		if _, err := ValidateProfileManifest(p, m); err != nil {
			t.Fatalf("effective disk size %d rejected: %v", size, err)
		}
	}
	legacy, err := LoadProfile(testutil.Write(t, filepath.Join(t.TempDir(), "agent.toml"), profileV1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateProfileManifest(legacy, m); err != nil {
		t.Fatalf("normalized version 1 profile rejected: %v", err)
	}
}

func TestManifestV5RejectsInvalidFoundationAndContradictions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Manifest, *Profile)
	}{
		{"wrong schema", func(m *Manifest, _ *Profile) { m.SchemaVersion = 4 }},
		{"multiple subnets in one AZ", func(m *Manifest, _ *Profile) { m.Subnets[1].AvailabilityZone = m.Subnets[0].AvailabilityZone }},
		{"malformed scope", func(m *Manifest, _ *Profile) { m.Account = "SECRET" }},
		{"unsupported region", func(m *Manifest, _ *Profile) { m.Region = "us-west-2" }},
		{"missing IAM pins", func(m *Manifest, _ *Profile) { m.Roles = nil }},
		{"missing runtime pins", func(m *Manifest, _ *Profile) { m.Execution = Execution{} }},
		{"missing ledger", func(m *Manifest, _ *Profile) { m.LaunchLedger = nil }},
		{"ledger schema", func(m *Manifest, _ *Profile) { m.LaunchLedger.SchemaVersion = 2 }},
		{"ledger bucket", func(m *Manifest, _ *Profile) { m.LaunchLedger.Bucket = "other-bucket" }},
		{"ledger owner", func(m *Manifest, _ *Profile) { m.LaunchLedger.ExpectedBucketOwner = "000000000000" }},
		{"ledger region", func(m *Manifest, _ *Profile) { m.LaunchLedger.Region = "us-west-2" }},
		{"ledger prefix", func(m *Manifest, _ *Profile) { m.LaunchLedger.Prefix = m.Results.Prefix }},
		{"ledger policy", func(m *Manifest, _ *Profile) { m.LaunchLedger.PolicySHA256 = strings.Repeat("b", 64) }},
		{"missing subnets", func(m *Manifest, _ *Profile) { m.SubnetIDs = nil }},
		{"duplicate subnet IDs", func(m *Manifest, _ *Profile) { m.SubnetIDs[1] = m.SubnetIDs[0] }},
		{"invalid subnet IDs", func(m *Manifest, _ *Profile) { m.SubnetIDs[0] = "SECRET" }},
		{"missing mapping", func(m *Manifest, _ *Profile) { m.Subnets = nil }},
		{"incomplete mapping", func(m *Manifest, _ *Profile) { m.Subnets = m.Subnets[:1] }},
		{"duplicate mapping", func(m *Manifest, _ *Profile) { m.Subnets[1] = m.Subnets[0] }},
		{"unlisted mapped subnet", func(m *Manifest, _ *Profile) { m.Subnets[0].ID = "subnet-11111111" }},
		{"unsupported mapped AZ", func(m *Manifest, _ *Profile) { m.Subnets[0].AvailabilityZone = "us-west-2a" }},
		{"missing offerings", func(m *Manifest, _ *Profile) { m.CompatiblePools = nil }},
		{"duplicate offering type", func(m *Manifest, _ *Profile) { m.CompatiblePools[1].InstanceType = m.CompatiblePools[0].InstanceType }},
		{"invalid offering type", func(m *Manifest, _ *Profile) { m.CompatiblePools[0].InstanceType = "SECRET" }},
		{"unsupported offering architecture", func(m *Manifest, _ *Profile) { m.CompatiblePools[0].Architecture = "arm64" }},
		{"empty offering subnets", func(m *Manifest, _ *Profile) { m.CompatiblePools[0].SubnetIDs = nil }},
		{"duplicate offering subnets", func(m *Manifest, _ *Profile) {
			m.CompatiblePools[0].SubnetIDs = []string{m.SubnetIDs[0], m.SubnetIDs[0]}
		}},
		{"unknown offering subnet", func(m *Manifest, _ *Profile) { m.CompatiblePools[0].SubnetIDs = []string{"subnet-11111111"} }},
		{"missing image", func(m *Manifest, _ *Profile) { m.Images = nil }},
		{"missing root mapping", func(m *Manifest, _ *Profile) { img := m.Images["agent"]; img.RootDisk = nil; m.Images["agent"] = img }},
		{"missing AMI minimum", func(m *Manifest, _ *Profile) {
			img := m.Images["agent"]
			img.MinimumRootDiskGB = 0
			m.Images["agent"] = img
		}},
		{"template smaller than minimum", func(m *Manifest, _ *Profile) {
			img := m.Images["agent"]
			img.MinimumRootDiskGB = 101
			m.Images["agent"] = img
		}},
		{"profile smaller than minimum", func(m *Manifest, p *Profile) {
			img := m.Images["agent"]
			img.MinimumRootDiskGB = 50
			m.Images["agent"] = img
			p.DiskGB = 49
		}},
		{"invalid template disk size", func(m *Manifest, _ *Profile) { m.Images["agent"].RootDisk.SizeGB = 7 }},
		{"unsupported template disk type", func(m *Manifest, _ *Profile) { m.Images["agent"].RootDisk.Type = "gp2" }},
		{"unencrypted template disk", func(m *Manifest, _ *Profile) { m.Images["agent"].RootDisk.Encrypted = false }},
		{"persistent template disk", func(m *Manifest, _ *Profile) { m.Images["agent"].RootDisk.DeleteOnTermination = false }},
		{"unsupported profile schema", func(_ *Manifest, p *Profile) { p.SchemaVersion = 7 }},
		{"unsupported profile architecture", func(_ *Manifest, p *Profile) { p.Architecture = "arm64" }},
		{"unsupported profile market", func(_ *Manifest, p *Profile) { p.Market = "automatic" }},
		{"unencrypted profile", func(_ *Manifest, p *Profile) { p.Encrypted = false }},
		{"duplicate profile types", func(_ *Manifest, p *Profile) { p.InstanceTypes = []string{"c7i.2xlarge", "c7i.2xlarge"} }},
		{"unknown profile type", func(_ *Manifest, p *Profile) { p.InstanceTypes = []string{"c6i.2xlarge"} }},
		{"unknown profile subnet", func(_ *Manifest, p *Profile) { p.SubnetIDs = []string{"subnet-11111111"} }},
		{"unknown profile zone", func(_ *Manifest, p *Profile) { p.AvailabilityZones = []string{"us-east-2c"} }},
		{"contradictory subnet and zone", func(_ *Manifest, p *Profile) {
			p.SubnetIDs = []string{"subnet-12345678"}
			p.AvailabilityZones = []string{"us-east-2b"}
		}},
		{"one type loses every offering", func(_ *Manifest, p *Profile) { p.AvailabilityZones = []string{"us-east-2a"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m, p := manifestV5(t)
			tc.change(&m, &p)
			if _, err := ValidateProfileManifest(p, m); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("expected safe pure validation rejection: %v", err)
			}
			writeManifest(t, c, m)
			if _, err := LoadManifest(c.Manifest, c, p); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("expected safe load rejection: %v", err)
			}
		})
	}
}

func TestManifestV5CompletedRecoveryDoesNotRequireLaunchPins(t *testing.T) {
	c, m, _ := manifestV5(t)
	m.LaunchLedger = nil
	m.Subnets = nil
	m.CompatiblePools = nil
	m.Images = nil
	m.Execution = Execution{}
	writeManifest(t, c, m)
	got, err := LoadResultManifest(c.Manifest, c)
	if err != nil || got != m.Results {
		t.Fatalf("completed logs require launch prerequisites: %+v, %v", got, err)
	}
	if _, err := LoadExecutionManifest(c.Manifest, c); err == nil {
		t.Fatal("new execution accepted without foundation pins")
	}
}

func TestManifestV4RejectsV5Options(t *testing.T) {
	for _, change := range []func(*Manifest){
		func(m *Manifest) { m.LaunchLedger = &LaunchLedger{} },
		func(m *Manifest) { m.Subnets = []Subnet{{ID: "subnet-12345678", AvailabilityZone: "us-east-2a"}} },
		func(m *Manifest) { m.CompatiblePools = []CompatiblePool{{InstanceType: "c7i.2xlarge"}} },
		func(m *Manifest) { img := m.Images["agent"]; img.RootDisk = &RootDisk{}; m.Images["agent"] = img },
		func(m *Manifest) { img := m.Images["agent"]; img.MinimumRootDiskGB = 8; m.Images["agent"] = img },
	} {
		c, err := Load(testutil.Setup(t), Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		var m Manifest
		if err := json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
			t.Fatal(err)
		}
		change(&m)
		writeManifest(t, c, m)
		if _, err := LoadExecutionManifest(c.Manifest, c); err == nil {
			t.Fatal("version 5 options accepted under version 4")
		}
	}
}
