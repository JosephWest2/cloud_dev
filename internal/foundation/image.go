package foundation

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func checkImage(ctx context.Context, api EC2, m config.Manifest, p config.Profile) error {
	fail := errors.New("pinned image or instance types are unavailable or incompatible; verify Canonical Ubuntu 24.04 provenance, x86_64 architecture and root disk size, then review the foundation and profile")
	if api == nil || len(p.InstanceTypes) == 0 {
		return fail
	}
	if m.SchemaVersion == 5 {
		if _, err := config.ValidateProfileManifest(p, m); err != nil {
			return fail
		}
	}
	img := m.Images[p.Image]
	out, err := api.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{img.AMIID}, Owners: []string{img.OwnerAccount}})
	if err != nil || out == nil || len(out.Images) != 1 {
		return fail
	}
	i := out.Images[0]
	if aws.ToString(i.ImageId) != img.AMIID || aws.ToString(i.OwnerId) != img.OwnerAccount || aws.ToString(i.Name) != img.Name || string(i.Architecture) != img.Architecture || i.State != types.ImageStateAvailable || i.RootDeviceType != types.DeviceTypeEbs || i.VirtualizationType != types.VirtualizationTypeHvm || aws.ToString(i.RootDeviceName) != img.RootDeviceName {
		return fail
	}
	root := 0
	for _, b := range i.BlockDeviceMappings {
		if aws.ToString(b.DeviceName) == img.RootDeviceName {
			if b.Ebs == nil || aws.ToInt32(b.Ebs.VolumeSize) < 1 || int(aws.ToInt32(b.Ebs.VolumeSize)) > p.DiskGB || (m.SchemaVersion == 5 && int(aws.ToInt32(b.Ebs.VolumeSize)) != img.MinimumRootDiskGB) {
				return fail
			}
			root++
		}
	}
	if root != 1 {
		return fail
	}
	selectedTypes := p.InstanceTypes
	if m.SchemaVersion == 5 {
		selectedTypes = make([]string, len(m.CompatiblePools))
		for n, pool := range m.CompatiblePools {
			selectedTypes[n] = pool.InstanceType
		}
	}
	names := make([]types.InstanceType, len(selectedTypes))
	wanted := map[string]bool{}
	for n, name := range selectedTypes {
		if wanted[name] {
			return fail
		}
		wanted[name] = true
		names[n] = types.InstanceType(name)
	}
	seen, tokens := map[string]bool{}, map[string]bool{}
	var token *string
	for {
		typesOut, err := api.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: names, NextToken: token})
		if err != nil || typesOut == nil {
			return fail
		}
		for _, t := range typesOut.InstanceTypes {
			name := string(t.InstanceType)
			if !wanted[name] || seen[name] || t.ProcessorInfo == nil || t.EbsInfo == nil {
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
			if m.SchemaVersion == 5 && !supportsFleetImage(t, i) {
				return fail
			}
			seen[name] = true
		}
		next := aws.ToString(typesOut.NextToken)
		if next == "" {
			break
		}
		if tokens[next] {
			return fail
		}
		tokens[next], token = true, typesOut.NextToken
	}
	for _, name := range selectedTypes {
		if !seen[name] {
			return fail
		}
	}
	if m.SchemaVersion == 5 {
		return checkOfferings(ctx, api, m)
	}
	return nil
}

func supportsFleetImage(t types.InstanceTypeInfo, image types.Image) bool {
	if t.EbsInfo == nil || t.EbsInfo.EncryptionSupport != types.EbsEncryptionSupportSupported || !slices.Contains(t.SupportedRootDeviceTypes, types.RootDeviceTypeEbs) || !slices.Contains(t.SupportedVirtualizationTypes, types.VirtualizationTypeHvm) || !slices.Contains(t.SupportedUsageClasses, types.UsageClassTypeSpot) || !slices.Contains(t.SupportedUsageClasses, types.UsageClassTypeOnDemand) || t.NetworkInfo == nil || aws.ToInt32(t.NetworkInfo.MaximumNetworkInterfaces) < 1 || aws.ToInt32(t.NetworkInfo.Ipv4AddressesPerInterface) < 1 {
		return false
	}
	if aws.ToBool(image.EnaSupport) && t.NetworkInfo.EnaSupport != types.EnaSupportSupported && t.NetworkInfo.EnaSupport != types.EnaSupportRequired {
		return false
	}
	// An explicitly pinned AMI boot mode must be supported by every pool type.
	switch image.BootMode {
	case types.BootModeValuesLegacyBios:
		return slices.Contains(t.SupportedBootModes, types.BootModeTypeLegacyBios)
	case types.BootModeValuesUefi:
		return slices.Contains(t.SupportedBootModes, types.BootModeTypeUefi)
	case types.BootModeValuesUefiPreferred:
		return slices.Contains(t.SupportedBootModes, types.BootModeTypeUefi) || slices.Contains(t.SupportedBootModes, types.BootModeTypeLegacyBios)
	case "":
		return true
	default:
		return false
	}
}

// Offerings establish supported type/AZ pairs, not available Spot capacity.
// Query each approved type with its own AZ set so no cartesian product can
// silently broaden the manifest's permitted combinations.
func checkOfferings(ctx context.Context, api EC2, m config.Manifest) error {
	fail := errors.New("approved instance-type/subnet offerings are unavailable, inaccessible or drifted; review the selected Ohio pools and re-export the foundation; this check does not measure Spot capacity")
	zones := map[string]string{}
	for _, subnet := range m.Subnets {
		zones[subnet.ID] = subnet.AvailabilityZone
	}
	for _, pool := range m.CompatiblePools {
		wanted := map[string]bool{}
		locations := make([]string, 0, len(pool.SubnetIDs))
		for _, subnet := range pool.SubnetIDs {
			zone := zones[subnet]
			if zone == "" || wanted[zone] {
				return fail
			}
			wanted[zone] = true
			locations = append(locations, zone)
		}
		seen, tokens := map[string]bool{}, map[string]bool{}
		var token *string
		for {
			out, err := api.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{LocationType: types.LocationTypeAvailabilityZone, Filters: []types.Filter{{Name: aws.String("instance-type"), Values: []string{pool.InstanceType}}, {Name: aws.String("location"), Values: locations}}, NextToken: token})
			if err != nil || out == nil {
				return fail
			}
			for _, offering := range out.InstanceTypeOfferings {
				zone := aws.ToString(offering.Location)
				if string(offering.InstanceType) != pool.InstanceType || offering.LocationType != types.LocationTypeAvailabilityZone || !wanted[zone] || seen[zone] {
					return fail
				}
				seen[zone] = true
			}
			next := aws.ToString(out.NextToken)
			if next == "" {
				break
			}
			if tokens[next] {
				return fail
			}
			tokens[next], token = true, out.NextToken
		}
		if len(seen) != len(wanted) || len(wanted) == 0 {
			return fail
		}
	}
	if len(m.CompatiblePools) == 0 {
		return fail
	}
	return nil
}

func checkTemplate(ctx context.Context, api EC2, m config.Manifest, p config.Profile) error {
	fail := errors.New("pinned launch template is missing, inaccessible or drifted; re-export after reviewing image, profile, network, bootstrap, encrypted/delete-on-termination root and IMDSv2 settings")
	if api == nil || len(m.SubnetIDs) == 0 {
		return fail
	}
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
	subnet := m.SubnetIDs[0]
	if m.SchemaVersion == 5 {
		subnet = ""
		if ni.DeviceIndex == nil || aws.ToInt32(ni.NetworkCardIndex) != 0 || aws.ToString(ni.PrivateIpAddress) != "" || len(ni.PrivateIpAddresses)+len(ni.Ipv6Addresses)+len(ni.Ipv4Prefixes)+len(ni.Ipv6Prefixes) != 0 || aws.ToInt32(ni.SecondaryPrivateIpAddressCount)+aws.ToInt32(ni.Ipv6AddressCount)+aws.ToInt32(ni.Ipv4PrefixCount)+aws.ToInt32(ni.Ipv6PrefixCount) != 0 || (aws.ToString(ni.InterfaceType) != "" && aws.ToString(ni.InterfaceType) != "interface") || d.Placement != nil || d.InstanceRequirements != nil || aws.ToBool(d.DisableApiTermination) || aws.ToBool(d.DisableApiStop) {
			return fail
		}
	}
	if aws.ToInt32(ni.DeviceIndex) != 0 || aws.ToString(ni.SubnetId) != subnet || len(ni.Groups) != 1 || ni.Groups[0] != m.SecurityGroupID || !aws.ToBool(ni.AssociatePublicIpAddress) || !aws.ToBool(ni.DeleteOnTermination) || aws.ToString(ni.NetworkInterfaceId) != "" {
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
	size := 100
	if m.SchemaVersion == 5 {
		if img.RootDisk == nil || img.MinimumRootDiskGB < 1 || img.RootDisk.SizeGB < img.MinimumRootDiskGB || img.RootDisk.Type != "gp3" || !img.RootDisk.Encrypted || !img.RootDisk.DeleteOnTermination {
			return fail
		}
		size = img.RootDisk.SizeGB
	}
	if !aws.ToBool(e.Encrypted) || !aws.ToBool(e.DeleteOnTermination) || e.VolumeType != types.VolumeTypeGp3 || int(aws.ToInt32(e.VolumeSize)) != size || aws.ToString(e.KmsKeyId) != "" || aws.ToString(e.SnapshotId) != "" {
		return fail
	}
	return nil
}
