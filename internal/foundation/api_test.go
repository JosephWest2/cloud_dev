package foundation

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

func (f *fake) DescribeVpcs(_ context.Context, input *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	if f.nilResponse == "DescribeVpcs" {
		return nil, nil
	}
	out := &ec2.DescribeVpcsOutput{}
	err := f.respond("DescribeVpcs", input, out)
	return out, err
}
func (f *fake) DescribeVpcAttribute(_ context.Context, input *ec2.DescribeVpcAttributeInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcAttributeOutput, error) {
	if f.nilResponse == "DescribeVpcAttribute" {
		return nil, nil
	}
	out := &ec2.DescribeVpcAttributeOutput{}
	err := f.respond("DescribeVpcAttribute", input, out)
	return out, err
}
func (f *fake) DescribeSubnets(_ context.Context, input *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	if f.nilResponse == "DescribeSubnets" {
		return nil, nil
	}
	out := &ec2.DescribeSubnetsOutput{}
	err := f.respond("DescribeSubnets", input, out)
	return out, err
}
func (f *fake) DescribeSecurityGroups(_ context.Context, input *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	if f.nilResponse == "DescribeSecurityGroups" {
		return nil, nil
	}
	out := &ec2.DescribeSecurityGroupsOutput{}
	err := f.respond("DescribeSecurityGroups", input, out)
	return out, err
}
func (f *fake) DescribeRouteTables(_ context.Context, input *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	if f.nilResponse == "DescribeRouteTables" {
		return nil, nil
	}
	out := &ec2.DescribeRouteTablesOutput{}
	err := f.respond("DescribeRouteTables", input, out)
	return out, err
}
func (f *fake) DescribeInternetGateways(_ context.Context, input *ec2.DescribeInternetGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	if f.nilResponse == "DescribeInternetGateways" {
		return nil, nil
	}
	out := &ec2.DescribeInternetGatewaysOutput{}
	err := f.respond("DescribeInternetGateways", input, out)
	return out, err
}
func (f *fake) DescribeImages(_ context.Context, input *ec2.DescribeImagesInput, _ ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	if f.nilResponse == "DescribeImages" {
		return nil, nil
	}
	out := &ec2.DescribeImagesOutput{}
	err := f.respond("DescribeImages", input, out)
	return out, err
}
func (f *fake) DescribeLaunchTemplateVersions(_ context.Context, input *ec2.DescribeLaunchTemplateVersionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
	if f.nilResponse == "DescribeLaunchTemplateVersions" {
		return nil, nil
	}
	out := &ec2.DescribeLaunchTemplateVersionsOutput{}
	err := f.respond("DescribeLaunchTemplateVersions", input, out)
	return out, err
}
func (f *fake) DescribeLaunchTemplates(_ context.Context, input *ec2.DescribeLaunchTemplatesInput, _ ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
	if f.nilResponse == "DescribeLaunchTemplates" {
		return nil, nil
	}
	out := &ec2.DescribeLaunchTemplatesOutput{}
	err := f.respond("DescribeLaunchTemplates", input, out)
	return out, err
}
func (f *fake) DescribeInstanceTypes(_ context.Context, input *ec2.DescribeInstanceTypesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error) {
	if f.nilResponse == "DescribeInstanceTypes" {
		return nil, nil
	}
	out := &ec2.DescribeInstanceTypesOutput{}
	err := f.respond("DescribeInstanceTypes", input, out)
	return out, err
}
func (f *fake) DescribeInstanceTypeOfferings(_ context.Context, input *ec2.DescribeInstanceTypeOfferingsInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstanceTypeOfferingsOutput, error) {
	if f.nilResponse == "DescribeInstanceTypeOfferings" {
		return nil, nil
	}
	out := &ec2.DescribeInstanceTypeOfferingsOutput{}
	err := f.respond("DescribeInstanceTypeOfferings", input, out)
	return out, err
}
func (f *fake) GetInstanceProfile(_ context.Context, input *iam.GetInstanceProfileInput, _ ...func(*iam.Options)) (*iam.GetInstanceProfileOutput, error) {
	if f.nilResponse == "GetInstanceProfile" {
		return nil, nil
	}
	out := &iam.GetInstanceProfileOutput{}
	err := f.respond("GetInstanceProfile", input, out)
	return out, err
}
func (f *fake) GetRole(_ context.Context, input *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	if f.nilResponse == "GetRole" {
		return nil, nil
	}
	out := &iam.GetRoleOutput{}
	err := f.respond("GetRole", input, out)
	return out, err
}
func (f *fake) ListRolePolicies(_ context.Context, input *iam.ListRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	if f.nilResponse == "ListRolePolicies" {
		return nil, nil
	}
	out := &iam.ListRolePoliciesOutput{}
	err := f.respond("ListRolePolicies", input, out)
	return out, err
}
func (f *fake) GetRolePolicy(_ context.Context, input *iam.GetRolePolicyInput, _ ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	if f.nilResponse == "GetRolePolicy" {
		return nil, nil
	}
	out := &iam.GetRolePolicyOutput{}
	err := f.respond("GetRolePolicy", input, out)
	return out, err
}
func (f *fake) ListAttachedRolePolicies(_ context.Context, input *iam.ListAttachedRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	if f.nilResponse == "ListAttachedRolePolicies" {
		return nil, nil
	}
	out := &iam.ListAttachedRolePoliciesOutput{}
	err := f.respond("ListAttachedRolePolicies", input, out)
	return out, err
}
func (f *fake) GetDocument(_ context.Context, input *ssm.GetDocumentInput, _ ...func(*ssm.Options)) (*ssm.GetDocumentOutput, error) {
	if f.nilResponse == "GetDocument" {
		return nil, nil
	}
	out := &ssm.GetDocumentOutput{}
	err := f.respond("GetDocument", input, out)
	return out, err
}
