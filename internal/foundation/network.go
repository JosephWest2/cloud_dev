package foundation

import (
	"context"
	"errors"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func scoped(tags []types.Tag, m config.Manifest) bool {
	values := map[string]string{}
	for _, t := range tags {
		values[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return values["ManagedBy"] == "devbox" && values["Deployment"] == m.Deployment && values["Owner"] == m.Owner
}

func checkNetwork(ctx context.Context, api EC2, m config.Manifest) error {
	fail := errors.New("network resources are missing, inaccessible or drifted; check foundation scope, DNS, route association, attached gateway and HTTP/HTTPS-only egress with no ingress; review a foundation plan")
	v, err := api.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{m.VPCID}})
	if err != nil || v == nil || len(v.Vpcs) != 1 {
		return fail
	}
	vpc := v.Vpcs[0]
	if aws.ToString(vpc.VpcId) != m.VPCID || aws.ToString(vpc.OwnerId) != m.Account || vpc.State != types.VpcStateAvailable || !scoped(vpc.Tags, m) {
		return fail
	}
	for _, attr := range []types.VpcAttributeName{types.VpcAttributeNameEnableDnsSupport, types.VpcAttributeNameEnableDnsHostnames} {
		out, err := api.DescribeVpcAttribute(ctx, &ec2.DescribeVpcAttributeInput{VpcId: aws.String(m.VPCID), Attribute: attr})
		if err != nil || out == nil || aws.ToString(out.VpcId) != m.VPCID {
			return fail
		}
		value := out.EnableDnsSupport
		if attr == types.VpcAttributeNameEnableDnsHostnames {
			value = out.EnableDnsHostnames
		}
		if value == nil || !aws.ToBool(value.Value) {
			return fail
		}
	}
	s, err := api.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: m.SubnetIDs})
	if err != nil || s == nil || len(s.Subnets) != 1 {
		return fail
	}
	sub := s.Subnets[0]
	if aws.ToString(sub.SubnetId) != m.SubnetIDs[0] || aws.ToString(sub.VpcId) != m.VPCID || aws.ToString(sub.OwnerId) != m.Account || sub.State != types.SubnetStateAvailable || !scoped(sub.Tags, m) || aws.ToBool(sub.MapPublicIpOnLaunch) {
		return fail
	}
	g, err := api.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{m.SecurityGroupID}})
	if err != nil || g == nil || len(g.SecurityGroups) != 1 {
		return fail
	}
	sg := g.SecurityGroups[0]
	if aws.ToString(sg.GroupId) != m.SecurityGroupID || aws.ToString(sg.VpcId) != m.VPCID || aws.ToString(sg.OwnerId) != m.Account || !scoped(sg.Tags, m) || len(sg.IpPermissions) != 0 || !expectedEgress(sg.IpPermissionsEgress) {
		return fail
	}
	r, err := api.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{m.RouteTableID}})
	if err != nil || r == nil || len(r.RouteTables) != 1 {
		return fail
	}
	rt := r.RouteTables[0]
	if aws.ToString(rt.RouteTableId) != m.RouteTableID || aws.ToString(rt.VpcId) != m.VPCID || aws.ToString(rt.OwnerId) != m.Account || !scoped(rt.Tags, m) {
		return fail
	}
	associated, internet := false, false
	for _, a := range rt.Associations {
		if aws.ToString(a.SubnetId) == m.SubnetIDs[0] && aws.ToString(a.RouteTableId) == m.RouteTableID && a.AssociationState != nil && a.AssociationState.State == types.RouteTableAssociationStateCodeAssociated {
			associated = true
		}
	}
	for _, route := range rt.Routes {
		if aws.ToString(route.DestinationCidrBlock) == "0.0.0.0/0" && aws.ToString(route.GatewayId) == m.InternetGatewayID && route.State == types.RouteStateActive {
			internet = true
			continue
		}
		if aws.ToString(route.GatewayId) != "local" || route.State != types.RouteStateActive {
			return fail
		}
	}
	if !associated || !internet {
		return fail
	}
	ig, err := api.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{InternetGatewayIds: []string{m.InternetGatewayID}})
	if err != nil || ig == nil || len(ig.InternetGateways) != 1 {
		return fail
	}
	gateway := ig.InternetGateways[0]
	if aws.ToString(gateway.InternetGatewayId) != m.InternetGatewayID || aws.ToString(gateway.OwnerId) != m.Account || !scoped(gateway.Tags, m) || len(gateway.Attachments) != 1 || aws.ToString(gateway.Attachments[0].VpcId) != m.VPCID || string(gateway.Attachments[0].State) != "available" {
		return fail
	}
	return nil
}

func expectedEgress(rules []types.IpPermission) bool {
	if len(rules) != 2 {
		return false
	}
	ports := map[int32]bool{}
	for _, r := range rules {
		from, to := aws.ToInt32(r.FromPort), aws.ToInt32(r.ToPort)
		if aws.ToString(r.IpProtocol) != "tcp" || from != to || (from != 80 && from != 443) || ports[from] || len(r.IpRanges) != 1 || aws.ToString(r.IpRanges[0].CidrIp) != "0.0.0.0/0" || len(r.Ipv6Ranges)+len(r.PrefixListIds)+len(r.UserIdGroupPairs) != 0 {
			return false
		}
		ports[from] = true
	}
	return true
}
