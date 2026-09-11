package foundation

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func checkImage(ctx context.Context, api EC2, m config.Manifest, p config.Profile) error {
	fail := errors.New("pinned image or instance types are unavailable or incompatible; verify Canonical Ubuntu 24.04 provenance, x86_64 architecture and root disk size, then review the foundation and profile")
	img := m.Images[p.Image]
	out, err := api.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{img.AMIID}, Owners: []string{img.OwnerAccount}})
	if err != nil || out == nil || len(out.Images) != 1 {
		return fail
	}
	i := out.Images[0]
	if aws.ToString(i.ImageId) != img.AMIID || aws.ToString(i.OwnerId) != img.OwnerAccount || aws.ToString(i.Name) != img.Name || string(i.Architecture) != img.Architecture || i.State != types.ImageStateAvailable || i.RootDeviceType != types.DeviceTypeEbs || i.VirtualizationType != types.VirtualizationTypeHvm || aws.ToString(i.RootDeviceName) != img.RootDeviceName {
		return fail
	}
	root := false
	for _, b := range i.BlockDeviceMappings {
		if aws.ToString(b.DeviceName) == img.RootDeviceName && b.Ebs != nil && aws.ToInt32(b.Ebs.VolumeSize) > 0 && int(aws.ToInt32(b.Ebs.VolumeSize)) <= p.DiskGB {
			root = true
		}
	}
	if !root {
		return fail
	}
	names := make([]types.InstanceType, len(p.InstanceTypes))
	for n, name := range p.InstanceTypes {
		names[n] = types.InstanceType(name)
	}
	seen := map[string]bool{}
	var token *string
	for {
		typesOut, err := api.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: names, NextToken: token})
		if err != nil || typesOut == nil {
			return fail
		}
		for _, t := range typesOut.InstanceTypes {
			if t.ProcessorInfo == nil || t.EbsInfo == nil {
				return fail
			}
			compatible := false
			for _, arch := range t.ProcessorInfo.SupportedArchitectures {
				if string(arch) == img.Architecture {
					compatible = true
				}
			}
			if !compatible {
				return fail
			}
			seen[string(t.InstanceType)] = true
		}
		next := aws.ToString(typesOut.NextToken)
		if next == "" {
			break
		}
		if next == aws.ToString(token) {
			return fail
		}
		token = typesOut.NextToken
	}
	for _, name := range p.InstanceTypes {
		if !seen[name] {
			return fail
		}
	}
	return nil
}

func checkTemplate(ctx context.Context, api EC2, m config.Manifest, p config.Profile) error {
	fail := errors.New("pinned launch template is missing, inaccessible or drifted; re-export after reviewing image, profile, network, bootstrap, encrypted/delete-on-termination root and IMDSv2 settings")
	img := m.Images[p.Image]
	templates, err := api.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{LaunchTemplateIds: []string{img.LaunchTemplateID}})
	if err != nil || templates == nil || len(templates.LaunchTemplates) != 1 || aws.ToString(templates.LaunchTemplates[0].LaunchTemplateId) != img.LaunchTemplateID || !scoped(templates.LaunchTemplates[0].Tags, m) {
		return fail
	}
	out, err := api.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{LaunchTemplateId: aws.String(img.LaunchTemplateID), Versions: []string{img.LaunchTemplateVersion}})
	if err != nil || out == nil || len(out.LaunchTemplateVersions) != 1 {
		return fail
	}
	version := out.LaunchTemplateVersions[0]
	if aws.ToString(version.LaunchTemplateId) != img.LaunchTemplateID || strconv.FormatInt(aws.ToInt64(version.VersionNumber), 10) != img.LaunchTemplateVersion || version.LaunchTemplateData == nil {
		return fail
	}
	d := version.LaunchTemplateData
	if aws.ToString(d.ImageId) != img.AMIID || d.IamInstanceProfile == nil || aws.ToString(d.IamInstanceProfile.Arn) != m.InstanceProfileARN || d.InstanceMarketOptions != nil || d.InstanceType != "" || aws.ToString(d.KeyName) != "" || len(d.SecurityGroupIds)+len(d.SecurityGroups)+len(d.TagSpecifications) != 0 {
		return fail
	}
	userData, err := base64.StdEncoding.DecodeString(aws.ToString(d.UserData))
	if err != nil || digest(userData) != m.BootstrapSHA256 {
		return fail
	}
	meta := d.MetadataOptions
	if meta == nil || meta.HttpTokens != types.LaunchTemplateHttpTokensStateRequired || meta.HttpEndpoint != types.LaunchTemplateInstanceMetadataEndpointStateEnabled || aws.ToInt32(meta.HttpPutResponseHopLimit) != 1 || meta.InstanceMetadataTags != types.LaunchTemplateInstanceMetadataTagsStateDisabled {
		return fail
	}
	if len(d.NetworkInterfaces) != 1 {
		return fail
	}
	ni := d.NetworkInterfaces[0]
	if aws.ToInt32(ni.DeviceIndex) != 0 || aws.ToString(ni.SubnetId) != m.SubnetIDs[0] || len(ni.Groups) != 1 || ni.Groups[0] != m.SecurityGroupID || !aws.ToBool(ni.AssociatePublicIpAddress) || !aws.ToBool(ni.DeleteOnTermination) || aws.ToString(ni.NetworkInterfaceId) != "" {
		return fail
	}
	if len(d.BlockDeviceMappings) != 1 {
		return fail
	}
	block := d.BlockDeviceMappings[0]
	if aws.ToString(block.DeviceName) != img.RootDeviceName || block.Ebs == nil || aws.ToString(block.NoDevice) != "" || aws.ToString(block.VirtualName) != "" {
		return fail
	}
	e := block.Ebs
	if !aws.ToBool(e.Encrypted) || !aws.ToBool(e.DeleteOnTermination) || e.VolumeType != types.VolumeTypeGp3 || aws.ToInt32(e.VolumeSize) != 100 || aws.ToString(e.KmsKeyId) != "" || aws.ToString(e.SnapshotId) != "" {
		return fail
	}
	return nil
}
