package foundation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

type fake struct {
	responses   map[string]string
	calls       map[string]int
	inputs      map[string]any
	fail        string
	nilResponse string
	hook        func(string, any) (string, bool)
}

func (f *fake) respond(name string, input, out any) error {
	f.calls[name]++
	f.inputs[name] = input
	if f.fail == name {
		return errors.New("SECRET provider response")
	}
	data := f.responses[name]
	if f.hook != nil {
		if custom, ok := f.hook(name, input); ok {
			data = custom
		}
	}
	return json.Unmarshal([]byte(data), out)
}
func fixture(t *testing.T) (*fake, config.Manifest, config.Profile) {
	t.Helper()
	var m config.Manifest
	if err := json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	p, err := config.LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:UpdateInstanceInformation"],"Resource":"*"}]}`
	hash, err := jsonDigest(policy)
	if err != nil {
		t.Fatal(err)
	}
	for key, role := range m.Roles {
		role.TrustSHA256 = hash
		role.PolicySHA256 = hash
		m.Roles[key] = role
	}
	m.BootstrapSHA256 = digest([]byte("bootstrap fixture\n"))
	m.Readiness.ContentSHA256 = hash
	m.Execution.ContentSHA256 = hash
	m.Results.PolicySHA256 = hash
	f := &fake{responses: map[string]string{}, calls: map[string]int{}, inputs: map[string]any{}}
	tags := `[{"Key":"ManagedBy","Value":"devbox"},{"Key":"Deployment","Value":"test"},{"Key":"Owner","Value":"test-owner"}]`
	f.responses["DescribeVpcs"] = `{"Vpcs":[{"VpcId":"vpc-12345678","OwnerId":"123456789012","State":"available","Tags":` + tags + `}]}`
	f.responses["DescribeVpcAttribute"] = `{"VpcId":"vpc-12345678","EnableDnsSupport":{"Value":true},"EnableDnsHostnames":{"Value":true}}`
	f.responses["DescribeSubnets"] = `{"Subnets":[{"SubnetId":"subnet-12345678","VpcId":"vpc-12345678","OwnerId":"123456789012","State":"available","MapPublicIpOnLaunch":false,"Tags":` + tags + `}]}`
	f.responses["DescribeSecurityGroups"] = `{"SecurityGroups":[{"GroupId":"sg-12345678","VpcId":"vpc-12345678","OwnerId":"123456789012","Tags":` + tags + `,"IpPermissions":[],"IpPermissionsEgress":[{"IpProtocol":"tcp","FromPort":80,"ToPort":80,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]},{"IpProtocol":"tcp","FromPort":443,"ToPort":443,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]}]}`
	f.responses["DescribeRouteTables"] = `{"RouteTables":[{"RouteTableId":"rtb-12345678","VpcId":"vpc-12345678","OwnerId":"123456789012","Tags":` + tags + `,"Associations":[{"SubnetId":"subnet-12345678","RouteTableId":"rtb-12345678","AssociationState":{"State":"associated"}}],"Routes":[{"GatewayId":"local","DestinationCidrBlock":"10.77.0.0/16","State":"active"},{"GatewayId":"igw-12345678","DestinationCidrBlock":"0.0.0.0/0","State":"active"}]}]}`
	f.responses["DescribeInternetGateways"] = `{"InternetGateways":[{"InternetGatewayId":"igw-12345678","OwnerId":"123456789012","Tags":` + tags + `,"Attachments":[{"VpcId":"vpc-12345678","State":"available"}]}]}`
	f.responses["DescribeImages"] = `{"Images":[{"ImageId":"ami-12345678","OwnerId":"099720109477","Name":"ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260901","Architecture":"x86_64","State":"available","RootDeviceType":"ebs","VirtualizationType":"hvm","RootDeviceName":"/dev/sda1","BlockDeviceMappings":[{"DeviceName":"/dev/sda1","Ebs":{"VolumeSize":8}}]}]}`
	f.responses["DescribeInstanceTypes"] = `{"InstanceTypes":[{"InstanceType":"c7i.2xlarge","ProcessorInfo":{"SupportedArchitectures":["x86_64"]},"EbsInfo":{}},{"InstanceType":"c7a.2xlarge","ProcessorInfo":{"SupportedArchitectures":["x86_64"]},"EbsInfo":{}},{"InstanceType":"c6i.2xlarge","ProcessorInfo":{"SupportedArchitectures":["x86_64"]},"EbsInfo":{}},{"InstanceType":"c6a.2xlarge","ProcessorInfo":{"SupportedArchitectures":["x86_64"]},"EbsInfo":{}}]}`
	f.responses["DescribeLaunchTemplates"] = `{"LaunchTemplates":[{"LaunchTemplateId":"lt-12345678","Tags":` + tags + `}]}`
	f.responses["DescribeLaunchTemplateVersions"] = `{"LaunchTemplateVersions":[{"LaunchTemplateId":"lt-12345678","VersionNumber":1,"LaunchTemplateData":{"ImageId":"ami-12345678","IamInstanceProfile":{"Arn":"arn:aws:iam::123456789012:instance-profile/devbox"},"UserData":"` + base64.StdEncoding.EncodeToString([]byte("bootstrap fixture\n")) + `","MetadataOptions":{"HttpTokens":"required","HttpEndpoint":"enabled","HttpPutResponseHopLimit":1,"InstanceMetadataTags":"disabled"},"NetworkInterfaces":[{"DeviceIndex":0,"SubnetId":"subnet-12345678","Groups":["sg-12345678"],"AssociatePublicIpAddress":true,"DeleteOnTermination":true}],"BlockDeviceMappings":[{"DeviceName":"/dev/sda1","Ebs":{"Encrypted":true,"DeleteOnTermination":true,"VolumeType":"gp3","VolumeSize":100}}]}}]}`
	f.responses["GetInstanceProfile"] = `{"InstanceProfile":{"Arn":"arn:aws:iam::123456789012:instance-profile/devbox","Roles":[{"Arn":"arn:aws:iam::123456789012:role/devbox-instance"}]}}`
	quoted, _ := json.Marshal(policy)
	f.responses["GetDocument"] = `{"Name":"devbox-test-readiness","DocumentVersion":"1","DocumentType":"Command","Status":"Active","Content":` + string(quoted) + `}`
	f.responses["GetBucketLocation"] = `{"LocationConstraint":"us-east-2"}`
	f.responses["GetBucketTagging"] = `{"TagSet":` + tags + `}`
	f.responses["GetBucketPolicy"] = `{"Policy":` + string(quoted) + `}`
	f.responses["GetPublicAccessBlock"] = `{"PublicAccessBlockConfiguration":{"BlockPublicAcls":true,"BlockPublicPolicy":true,"IgnorePublicAcls":true,"RestrictPublicBuckets":true}}`
	f.responses["GetBucketEncryption"] = `{"ServerSideEncryptionConfiguration":{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}}`
	f.responses["GetBucketOwnershipControls"] = `{"OwnershipControls":{"Rules":[{"ObjectOwnership":"BucketOwnerEnforced"}]}}`
	f.responses["GetBucketVersioning"] = `{}`
	f.responses["GetBucketLifecycleConfiguration"] = `{"Rules":[{"ID":"command-results-retention","Status":"Enabled","Filter":{"Prefix":"` + m.Results.Prefix + `"},"Expiration":{"Days":30},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":1}}]}`
	f.responses["HeadObject"] = `{"ContentLength":1024,"ServerSideEncryption":"AES256","Metadata":{"sha256":"` + m.Execution.RunnerSHA256 + `"}}`
	f.responses["ListAttachedRolePolicies"] = `{"AttachedPolicies":[]}`
	// Both roles use the same document fixture, but requests must select their exact role/policy.
	f.hook = func(name string, input any) (string, bool) {
		b, _ := json.Marshal(input)
		var fields map[string]any
		_ = json.Unmarshal(b, &fields)
		role, _ := fields["RoleName"].(string)
		switch name {
		case "GetDocument":
			if fields["Name"] == m.Execution.Name {
				return strings.ReplaceAll(f.responses[name], m.Readiness.Name, m.Execution.Name), true
			}
		case "GetRole":
			return `{"Role":{"Arn":"arn:aws:iam::123456789012:role/` + role + `","AssumeRolePolicyDocument":` + string(quoted) + `,"Tags":` + tags + `}}`, true
		case "ListRolePolicies":
			return `{"PolicyNames":["` + role + `"]}`, true
		case "GetRolePolicy":
			return `{"RoleName":"` + role + `","PolicyName":"` + role + `","PolicyDocument":` + string(quoted) + `}`, true
		}
		return "", false
	}
	return f, m, p
}

func TestDeployedResourcesAndExactRequests(t *testing.T) {
	f, m, p := fixture(t)
	for _, check := range Verify(context.Background(), Clients{f, f, f, f}, m, p) {
		if check.Err != nil {
			t.Fatalf("%s: %v", check.Name, check.Err)
		}
	}
	for _, name := range []string{"DescribeLaunchTemplateVersions", "GetDocument", "DescribeImages"} {
		b, _ := json.Marshal(f.inputs[name])
		for _, forbidden := range []string{"$Latest", "$Default", "current"} {
			if strings.Contains(string(b), forbidden) {
				t.Fatal("unpinned request", string(b))
			}
		}
	}
	b, _ := json.Marshal(f.inputs["DescribeLaunchTemplateVersions"])
	if !strings.Contains(string(b), `"Versions":["1"]`) {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(f.inputs["GetDocument"])
	if !strings.Contains(string(b), `"DocumentVersion":"1"`) {
		t.Fatal(string(b))
	}
	if f.calls["GetRole"] != 2 || f.calls["DescribeVpcAttribute"] != 2 {
		t.Fatal(f.calls)
	}
}

func TestResourceDrift(t *testing.T) {
	for _, tc := range []struct{ name, api, old, new string }{
		{"foreign VPC", "DescribeVpcs", "123456789012", "000000000000"},
		{"missing scope tags", "DescribeSubnets", "test-owner", "someone-else"},
		{"DNS disabled", "DescribeVpcAttribute", `"Value":true`, `"Value":false`},
		{"ingress", "DescribeSecurityGroups", `"IpPermissions":[]`, `"IpPermissions":[{"IpProtocol":"-1"}]`},
		{"broad egress", "DescribeSecurityGroups", `"FromPort":80,"ToPort":80`, `"FromPort":0,"ToPort":65535`},
		{"route detached", "DescribeRouteTables", "associated", "disassociated"},
		{"route blackhole", "DescribeRouteTables", `"State":"active"`, `"State":"blackhole"`},
		{"foreign gateway", "DescribeInternetGateways", "vpc-12345678", "vpc-87654321"},
		{"untrusted image", "DescribeImages", "099720109477", "000000000000"},
		{"wrong release", "DescribeImages", "24.04", "22.04"},
		{"oversized snapshot", "DescribeImages", `"VolumeSize":8`, `"VolumeSize":101`},
		{"wrong architecture", "DescribeInstanceTypes", "x86_64", "arm64"},
		{"wrong template version", "DescribeLaunchTemplateVersions", `"VersionNumber":1`, `"VersionNumber":2`},
		{"unencrypted", "DescribeLaunchTemplateVersions", `"Encrypted":true`, `"Encrypted":false`},
		{"retained disk", "DescribeLaunchTemplateVersions", `"DeleteOnTermination":true`, `"DeleteOnTermination":false`},
		{"IMDSv1", "DescribeLaunchTemplateVersions", "required", "optional"},
		{"changed bootstrap", "DescribeLaunchTemplateVersions", base64.StdEncoding.EncodeToString([]byte("bootstrap fixture\n")), "c2VjcmV0"},
		{"wrong security group", "DescribeLaunchTemplateVersions", "sg-12345678", "sg-87654321"},
		{"wrong instance role", "GetInstanceProfile", "role/devbox-instance", "role/admin"},
		{"extra managed policy", "ListAttachedRolePolicies", `"AttachedPolicies":[]`, `"AttachedPolicies":[{"PolicyArn":"arn:aws:iam::aws:policy/AdministratorAccess"}]`},
		{"wrong probe version", "GetDocument", `"DocumentVersion":"1"`, `"DocumentVersion":"2"`},
		{"changed probe content", "GetDocument", "UpdateInstanceInformation", "SendCommand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, p := fixture(t)
			f.responses[tc.api] = strings.ReplaceAll(f.responses[tc.api], tc.old, tc.new)
			failed := false
			for _, check := range Verify(context.Background(), Clients{f, f, f, f}, m, p) {
				if check.Err != nil {
					failed = true
				}
			}
			if !failed {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestAPIFailuresAndNilResponses(t *testing.T) {
	f, _, _ := fixture(t)
	apis := []string{"GetRole", "ListRolePolicies", "GetRolePolicy"}
	for name := range f.responses {
		apis = append(apis, name)
	}
	for _, name := range apis {
		for _, nilResponse := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				f, m, p := fixture(t)
				if nilResponse {
					f.nilResponse = name
				} else {
					f.fail = name
				}
				failed := false
				for _, check := range Verify(context.Background(), Clients{f, f, f, f}, m, p) {
					if check.Err != nil {
						failed = true
						if strings.Contains(check.Err.Error(), "SECRET") {
							t.Fatal("leak")
						}
					}
				}
				if !failed {
					t.Fatal("missing API evidence accepted")
				}
			})
		}
	}
}

func TestCanceledChecksDoNotCallAWS(t *testing.T) {
	f, m, p := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, check := range Verify(ctx, Clients{f, f, f, f}, m, p) {
		if !errors.Is(check.Err, context.Canceled) {
			t.Fatal(check)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
}

func TestIAMCanonicalDigest(t *testing.T) {
	// Golden value from OpenTofu jsonencode, including HTML escaping and a literal '+'.
	canonical := `{"Principal":{"AWS":"arn:aws:iam::123456789012:role/A+B"},"Text":"\u003c\u0026\u003e","Version":"2012-10-17"}`
	expected := digest([]byte(canonical))
	for _, input := range []string{canonical, `{ "Version":"2012-10-17", "Text":"<&>", "Principal":{"AWS":"arn:aws:iam::123456789012:role/A+B"}}`, url.PathEscape(canonical)} {
		got, err := jsonDigest(input)
		if err != nil || got != expected {
			t.Fatalf("%s %v", got, err)
		}
	}
}

func TestIAMPaginationRejectsHiddenPolicies(t *testing.T) {
	for _, api := range []string{"ListRolePolicies", "ListAttachedRolePolicies"} {
		f, m, p := fixture(t)
		original := f.hook
		f.hook = func(name string, input any) (string, bool) {
			if name == api {
				if f.calls[name] == 1 {
					return `{"IsTruncated":true,"Marker":"page2"}`, true
				}
				if api == "ListRolePolicies" {
					return `{"PolicyNames":["Administrator"]}`, true
				}
				return `{"AttachedPolicies":[{"PolicyArn":"arn:aws:iam::aws:policy/AdministratorAccess"}]}`, true
			}
			return original(name, input)
		}
		if checkIAM(context.Background(), f, m) == nil || f.calls[api] != 2 {
			t.Fatal("pagination missed policy", api, f.calls)
		}
		_ = p
	}
}
