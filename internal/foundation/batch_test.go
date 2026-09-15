package foundation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

func batchFixture(t *testing.T) (*fake, config.Manifest, config.Profile) {
	t.Helper()
	f, m, p := fixture(t)
	m.SchemaVersion = 5
	m.SubnetIDs = []string{"subnet-12345678", "subnet-87654321"}
	m.Subnets = []config.Subnet{{ID: m.SubnetIDs[0], AvailabilityZone: "us-east-2a"}, {ID: m.SubnetIDs[1], AvailabilityZone: "us-east-2b"}}
	for index, name := range p.InstanceTypes {
		subnets := m.SubnetIDs
		if index%2 != 0 {
			subnets = subnets[1:]
		}
		m.CompatiblePools = append(m.CompatiblePools, config.CompatiblePool{InstanceType: name, Architecture: "x86_64", SubnetIDs: subnets})
	}
	img := m.Images["agent"]
	img.RootDisk = &config.RootDisk{SizeGB: 100, Type: "gp3", Encrypted: true, DeleteOnTermination: true}
	img.MinimumRootDiskGB = 8
	m.Images["agent"] = img
	m.LaunchLedger = &config.LaunchLedger{SchemaVersion: 1, Bucket: m.Results.Bucket, ExpectedBucketOwner: m.Account, Region: m.Region, Prefix: config.LaunchLedgerPrefix(config.Config{ExpectedAccount: m.Account, Region: m.Region, Deployment: m.Deployment, Owner: m.Owner})}
	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"RequireImmutableLaunchCreation","Effect":"Deny","Principal":"*","Action":"s3:PutObject","Resource":"arn:aws:s3:::` + m.Results.Bucket + `/` + m.LaunchLedger.Prefix + `*","Condition":{"StringNotEquals":{"s3:if-none-match":"*"}}}]}`
	m.Results.PolicySHA256, _ = jsonDigest(policy)
	m.LaunchLedger.PolicySHA256 = m.Results.PolicySHA256
	quoted, _ := json.Marshal(policy)
	f.responses["GetBucketPolicy"] = `{"Policy":` + string(quoted) + `}`
	var subnets map[string][]map[string]any
	_ = json.Unmarshal([]byte(f.responses["DescribeSubnets"]), &subnets)
	first := subnets["Subnets"][0]
	first["AvailabilityZone"] = "us-east-2a"
	second := map[string]any{}
	for key, value := range first {
		second[key] = value
	}
	second["SubnetId"], second["AvailabilityZone"] = m.SubnetIDs[1], "us-east-2b"
	subnets["Subnets"] = append(subnets["Subnets"], second)
	data, _ := json.Marshal(subnets)
	f.responses["DescribeSubnets"] = string(data)
	f.responses["DescribeRouteTables"] = strings.Replace(f.responses["DescribeRouteTables"], `"Associations":[`, `"Associations":[{"SubnetId":"subnet-87654321","RouteTableId":"rtb-12345678","AssociationState":{"State":"associated"}},`, 1)
	f.responses["DescribeLaunchTemplateVersions"] = strings.Replace(f.responses["DescribeLaunchTemplateVersions"], `"SubnetId":"subnet-12345678",`, "", 1)
	f.responses["DescribeImages"] = strings.Replace(f.responses["DescribeImages"], `"Architecture":"x86_64",`, `"Architecture":"x86_64","EnaSupport":true,"BootMode":"uefi",`, 1)
	f.responses["DescribeInstanceTypes"] = strings.ReplaceAll(f.responses["DescribeInstanceTypes"], `"EbsInfo":{}`, `"EbsInfo":{"EncryptionSupport":"supported"},"SupportedRootDeviceTypes":["ebs"],"SupportedUsageClasses":["spot","on-demand"],"SupportedVirtualizationTypes":["hvm"],"SupportedBootModes":["uefi"],"NetworkInfo":{"EnaSupport":"required","MaximumNetworkInterfaces":3,"Ipv4AddressesPerInterface":10}`)
	f.responses["DescribeInstanceTypeOfferings"] = `{}`
	original := f.hook
	f.hook = func(name string, input any) (string, bool) {
		if name == "GetRole" && aws.ToString(input.(*iam.GetRoleInput).RoleName) == "AWSServiceRoleForEC2Spot" {
			return `{"Role":{"Arn":"arn:aws:iam::123456789012:role/aws-service-role/spot.amazonaws.com/AWSServiceRoleForEC2Spot","AssumeRolePolicyDocument":"{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"sts:AssumeRole\",\"Principal\":{\"Service\":\"spot.amazonaws.com\"}}]}"}}`, true
		}
		if name == "DescribeInstanceTypeOfferings" {
			in := input.(*ec2.DescribeInstanceTypeOfferingsInput)
			if in.LocationType != "availability-zone" || len(in.Filters) != 2 || aws.ToString(in.Filters[0].Name) != "instance-type" || len(in.Filters[0].Values) != 1 || aws.ToString(in.Filters[1].Name) != "location" {
				t.Fatal("unbound offerings request", in)
			}
			var offerings []map[string]string
			for _, zone := range in.Filters[1].Values {
				offerings = append(offerings, map[string]string{"InstanceType": in.Filters[0].Values[0], "Location": zone, "LocationType": "availability-zone"})
			}
			b, _ := json.Marshal(map[string]any{"InstanceTypeOfferings": offerings})
			return string(b), true
		}
		return original(name, input)
	}
	return f, m, p
}

func TestBatchDeployedResourcesAndExactPools(t *testing.T) {
	f, m, p := batchFixture(t)
	for _, check := range Verify(context.Background(), Clients{f, f, f, f}, m, p) {
		if check.Err != nil {
			t.Fatalf("%s: %v", check.Name, check.Err)
		}
	}
	if f.calls["DescribeInstanceTypeOfferings"] != len(m.CompatiblePools) || f.calls["GetRole"] != 3 {
		t.Fatal(f.calls)
	}
	input := f.inputs["DescribeSubnets"].(*ec2.DescribeSubnetsInput)
	if strings.Join(input.SubnetIds, ",") != strings.Join(m.SubnetIDs, ",") {
		t.Fatal("subnets not exact", input)
	}
	inputOffer := f.inputs["DescribeInstanceTypeOfferings"].(*ec2.DescribeInstanceTypeOfferingsInput)
	if len(inputOffer.Filters[1].Values) != 1 || inputOffer.Filters[1].Values[0] != "us-east-2b" {
		t.Fatal("approved pool was expanded to a cartesian product", inputOffer)
	}
}

func TestBatchResourceDrift(t *testing.T) {
	for _, tc := range []struct{ name, api, old, new, check string }{
		{"wrong AZ", "DescribeSubnets", "us-east-2b", "us-east-2c", "foundation_network"},
		{"foreign second subnet", "DescribeSubnets", "subnet-87654321", "subnet-22222222", "foundation_network"},
		{"duplicate subnet", "DescribeSubnets", "subnet-87654321", "subnet-12345678", "foundation_network"},
		{"foreign subnet VPC", "DescribeSubnets", "vpc-12345678", "vpc-22222222", "foundation_network"},
		{"missing second route", "DescribeRouteTables", "subnet-87654321", "subnet-22222222", "foundation_network"},
		{"root minimum drift", "DescribeImages", `"VolumeSize":8`, `"VolumeSize":9`, "foundation_image"},
		{"unsupported EBS encryption", "DescribeInstanceTypes", `"EncryptionSupport":"supported"`, `"EncryptionSupport":"unsupported"`, "foundation_image"},
		{"unsupported EBS root", "DescribeInstanceTypes", `"SupportedRootDeviceTypes":["ebs"]`, `"SupportedRootDeviceTypes":["instance-store"]`, "foundation_image"},
		{"missing On-Demand", "DescribeInstanceTypes", `"spot","on-demand"`, `"spot"`, "foundation_image"},
		{"missing Spot", "DescribeInstanceTypes", `"spot","on-demand"`, `"on-demand"`, "foundation_image"},
		{"no HVM", "DescribeInstanceTypes", `"hvm"`, `"paravirtual"`, "foundation_image"},
		{"no ENA", "DescribeInstanceTypes", `"EnaSupport":"required"`, `"EnaSupport":"unsupported"`, "foundation_image"},
		{"no IPv4", "DescribeInstanceTypes", `"Ipv4AddressesPerInterface":10`, `"Ipv4AddressesPerInterface":0`, "foundation_image"},
		{"wrong boot mode", "DescribeInstanceTypes", `"SupportedBootModes":["uefi"]`, `"SupportedBootModes":["legacy-bios"]`, "foundation_image"},
		{"foreign type", "DescribeInstanceTypes", "c7i.2xlarge", "c8i.2xlarge", "foundation_image"},
		{"duplicate type", "DescribeInstanceTypes", "c7a.2xlarge", "c7i.2xlarge", "foundation_image"},
		{"fixed template subnet", "DescribeLaunchTemplateVersions", `"DeviceIndex":0`, `"DeviceIndex":0,"SubnetId":"subnet-12345678"`, "foundation_template"},
		{"fixed template placement", "DescribeLaunchTemplateVersions", `"LaunchTemplateData":{`, `"LaunchTemplateData":{"Placement":{"AvailabilityZone":"us-east-2a"},`, "foundation_template"},
		{"fixed private address", "DescribeLaunchTemplateVersions", `"DeviceIndex":0`, `"DeviceIndex":0,"PrivateIpAddress":"10.77.1.2"`, "foundation_template"},
		{"wrong template default", "DescribeLaunchTemplateVersions", `"VolumeSize":100`, `"VolumeSize":101`, "foundation_template"},
		{"ledger expiration", "GetBucketLifecycleConfiguration", `"Rules":[`, `"Rules":[{"Status":"Enabled","Filter":{"Prefix":"launches/v2/"},"Expiration":{"Days":365}},`, "foundation_launch_ledger"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, p := batchFixture(t)
			before := f.responses[tc.api]
			f.responses[tc.api] = strings.ReplaceAll(before, tc.old, tc.new)
			if before == f.responses[tc.api] {
				t.Fatal("fixture unchanged")
			}
			for _, check := range Verify(context.Background(), Clients{f, f, f, f}, m, p) {
				if check.Name == tc.check {
					if check.Err == nil {
						t.Fatal("drift accepted")
					}
					return
				}
			}
			t.Fatal("missing expected check")
		})
	}
}

func TestInvalidBatchConfigurationMakesNoAWSRequests(t *testing.T) {
	for _, mutate := range []func(*config.Manifest, *config.Profile){
		func(m *config.Manifest, _ *config.Profile) { m.SubnetIDs = nil },
		func(m *config.Manifest, _ *config.Profile) { m.CompatiblePools = nil },
		func(m *config.Manifest, _ *config.Profile) { m.LaunchLedger = nil },
		func(_ *config.Manifest, p *config.Profile) { p.InstanceTypes = nil },
		func(_ *config.Manifest, p *config.Profile) { p.DiskGB = 7 },
		func(_ *config.Manifest, p *config.Profile) {
			p.SchemaVersion = 2
			p.SubnetIDs = []string{"subnet-11111111"}
		},
	} {
		f, m, p := batchFixture(t)
		mutate(&m, &p)
		checks := Verify(context.Background(), Clients{f, f, f, f}, m, p)
		if len(checks) != 1 || checks[0].Name != "foundation_configuration" || checks[0].Err == nil || len(f.calls) != 0 {
			t.Fatal(checks, f.calls)
		}
	}
}

func TestBatchTemplateUsesExportedDefaultAndAllowsSmallerProfile(t *testing.T) {
	f, m, p := batchFixture(t)
	m.Images["agent"].RootDisk.SizeGB = 120
	p.DiskGB = 20
	f.responses["DescribeLaunchTemplateVersions"] = strings.Replace(f.responses["DescribeLaunchTemplateVersions"], `"VolumeSize":100`, `"VolumeSize":120`, 1)
	for _, check := range Verify(context.Background(), Clients{f, f, f, f}, m, p) {
		if check.Err != nil {
			t.Fatal(check.Name, check.Err)
		}
	}
}

func TestBatchPreservesUnselectedLegacySubnetAssociation(t *testing.T) {
	f, m, _ := batchFixture(t)
	m.SubnetIDs, m.Subnets = m.SubnetIDs[1:], m.Subnets[1:]
	var response map[string][]map[string]any
	_ = json.Unmarshal([]byte(f.responses["DescribeSubnets"]), &response)
	response["Subnets"] = response["Subnets"][1:]
	b, _ := json.Marshal(response)
	f.responses["DescribeSubnets"] = string(b)
	if err := checkNetwork(context.Background(), f, m); err != nil {
		t.Fatal(err)
	}
}

func TestBatchReadPaginationAndTokenCycles(t *testing.T) {
	for _, tc := range []struct{ api, field string }{{"DescribeSubnets", "Subnets"}, {"DescribeInstanceTypes", "InstanceTypes"}, {"DescribeInstanceTypeOfferings", "InstanceTypeOfferings"}} {
		for _, cycle := range []bool{false, true} {
			t.Run(tc.api+map[bool]string{false: "/pages", true: "/cycle"}[cycle], func(t *testing.T) {
				f, m, p := batchFixture(t)
				original := f.hook
				f.hook = func(name string, input any) (string, bool) {
					if name != tc.api {
						return original(name, input)
					}
					encoded, _ := json.Marshal(input)
					var request map[string]any
					_ = json.Unmarshal(encoded, &request)
					if cycle {
						next := "second"
						if request["NextToken"] == "second" {
							next = "third"
						}
						return `{"NextToken":"` + next + `"}`, true
					}
					data, ok := original(name, input)
					if !ok {
						data = f.responses[name]
					}
					var response map[string]any
					_ = json.Unmarshal([]byte(data), &response)
					items := response[tc.field].([]any)
					if request["NextToken"] == nil {
						response[tc.field] = items[:1]
						response["NextToken"] = "second"
					} else {
						if request["NextToken"] != "second" {
							t.Fatal("incorrect next token", request)
						}
						response[tc.field] = items[1:]
					}
					b, _ := json.Marshal(response)
					return string(b), true
				}
				var err error
				if tc.api == "DescribeSubnets" {
					err = checkNetwork(context.Background(), f, m)
				} else {
					err = checkImage(context.Background(), f, m, p)
				}
				if (err != nil) != cycle {
					t.Fatal("unexpected pagination result", err)
				}
				want := 2
				if tc.api == "DescribeInstanceTypeOfferings" {
					want *= len(m.CompatiblePools)
				}
				if cycle {
					want = 3
				}
				if f.calls[tc.api] != want {
					t.Fatal(f.calls)
				}
			})
		}
	}
}

func TestBatchOfferingsRejectMissingForeignDuplicateOrInaccessibleEvidence(t *testing.T) {
	for _, mutation := range []string{"empty", "foreign-type", "foreign-zone", "region-location", "duplicate", "missing", "denied", "nil"} {
		t.Run(mutation, func(t *testing.T) {
			f, m, p := batchFixture(t)
			original := f.hook
			f.hook = func(name string, input any) (string, bool) {
				data, ok := original(name, input)
				if name != "DescribeInstanceTypeOfferings" {
					return data, ok
				}
				switch mutation {
				case "empty":
					return `{}`, true
				case "foreign-type":
					return strings.ReplaceAll(data, "c7i.2xlarge", "c8i.2xlarge"), true
				case "foreign-zone":
					return strings.ReplaceAll(data, "us-east-2b", "us-east-2c"), true
				case "region-location":
					return strings.ReplaceAll(data, "availability-zone", "region"), true
				case "duplicate", "missing":
					var response map[string][]map[string]any
					_ = json.Unmarshal([]byte(data), &response)
					items := response["InstanceTypeOfferings"]
					if mutation == "duplicate" {
						items = append(items, items[0])
					} else {
						items = items[:1]
					}
					response["InstanceTypeOfferings"] = items
					b, _ := json.Marshal(response)
					return string(b), true
				}
				return data, ok
			}
			if mutation == "denied" {
				f.fail = "DescribeInstanceTypeOfferings"
			}
			if mutation == "nil" {
				f.nilResponse = "DescribeInstanceTypeOfferings"
			}
			err := checkImage(context.Background(), f, m, p)
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("offering evidence accepted or error leaked", err)
			}
		})
	}
}

func TestBatchVerifiesAllExportedPoolsDespiteProfileRestrictions(t *testing.T) {
	f, m, p := batchFixture(t)
	p.SchemaVersion = 2
	p.InstanceTypes = p.InstanceTypes[:1]
	p.AvailabilityZones = []string{"us-east-2a"}
	if err := checkImage(context.Background(), f, m, p); err != nil {
		t.Fatal(err)
	}
	if f.calls["DescribeInstanceTypeOfferings"] != len(m.CompatiblePools) {
		t.Fatal("unchecked exported pools", f.calls)
	}
}

func TestLaunchLedgerRequiresConditionalWritesEvenAfterReexport(t *testing.T) {
	for _, replacement := range []string{"s3:if-match", "s3:prefix"} {
		f, m, _ := batchFixture(t)
		var response struct{ Policy string }
		_ = json.Unmarshal([]byte(f.responses["GetBucketPolicy"]), &response)
		response.Policy = strings.ReplaceAll(response.Policy, "s3:if-none-match", replacement)
		m.Results.PolicySHA256, _ = jsonDigest(response.Policy)
		m.LaunchLedger.PolicySHA256 = m.Results.PolicySHA256
		b, _ := json.Marshal(response)
		f.responses["GetBucketPolicy"] = string(b)
		if err := checkLaunchLedger(context.Background(), f, m); err == nil {
			t.Fatal("mutable ledger accepted")
		}
	}
}

func TestSpotServiceRoleRequiresExactAccountServiceAndTrust(t *testing.T) {
	for _, tc := range []struct{ old, new string }{{"123456789012", "111111111111"}, {"spot.amazonaws.com", "ec2.amazonaws.com"}, {"Allow", "Deny"}, {"sts:AssumeRole", "sts:*"}} {
		f, m, _ := batchFixture(t)
		original := f.hook
		f.hook = func(name string, input any) (string, bool) {
			data, ok := original(name, input)
			if name == "GetRole" && aws.ToString(input.(*iam.GetRoleInput).RoleName) == "AWSServiceRoleForEC2Spot" {
				return strings.ReplaceAll(data, tc.old, tc.new), true
			}
			return data, ok
		}
		if err := checkIAM(context.Background(), f, m); err == nil {
			t.Fatal("invalid Spot prerequisite accepted", tc)
		}
	}
}

// A v6 reader must keep every v5 launch check. Cleanup health is independent
// and is implemented by #46; this test does not attest to scheduled deployment.
func TestExpiryManifestPreservesLaunchVerification(t *testing.T) {
	for _, drift := range []bool{false, true} {
		f, m, p := batchFixture(t)
		m.SchemaVersion = 6
		m.Cleanup = json.RawMessage(`{"execution_role":null}`)
		if drift {
			f.responses["DescribeImages"] = strings.Replace(f.responses["DescribeImages"], `"VolumeSize":8`, `"VolumeSize":9`, 1)
		}
		failed := false
		for _, check := range Verify(context.Background(), Clients{f, f, f, f}, m, p) {
			failed = failed || check.Err != nil
		}
		if failed != drift {
			t.Fatalf("v6 launch checks: drift=%t failure=%t", drift, failed)
		}
		if !drift && (f.calls["DescribeInstanceTypeOfferings"] != len(m.CompatiblePools) || f.calls["GetRole"] != 3) {
			t.Fatal("v6 skipped Fleet verification", f.calls)
		}
	}
}
