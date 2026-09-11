locals {
  instance_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole"
  }] })
  operator_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect    = "Allow", Principal = { AWS = var.operator_principal_arn }, Action = "sts:AssumeRole"
    Condition = { StringEquals = { "sts:RoleSessionName" = local.name } }
  }] })
  instance_policy = jsonencode({ Version = "2012-10-17", Statement = [{
    Sid       = "AgentTransport", Effect = "Allow"
    Action    = ["ssm:UpdateInstanceInformation", "ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel", "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel"]
    Resource  = "*"
    Condition = { StringEquals = { "aws:RequestedRegion" = var.region } }
  }] })
  request_scope  = { for k, v in local.tags : "aws:RequestTag/${k}" => v }
  resource_scope = { for k, v in local.tags : "ec2:ResourceTag/${k}" => v }
  ssm_scope      = { for k, v in local.tags : "ssm:resourceTag/${k}" => v }
  creation_tags  = ["ManagedBy", "Deployment", "Owner", "Profile", "Name", "RequestId", "CreatedAt"]
  launch_condition = {
    ArnEquals    = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn }
    StringEquals = { "aws:RequestedRegion" = var.region }
  }
  operator_policy = jsonencode({ Version = "2012-10-17", Statement = [
    {
      Sid       = "RegionalInventory", Effect = "Allow", Resource = "*"
      Action    = ["ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:DescribeVpcs", "ec2:DescribeVpcAttribute", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups", "ec2:DescribeRouteTables", "ec2:DescribeInternetGateways", "ec2:DescribeImages", "ec2:DescribeLaunchTemplateVersions", "ec2:DescribeLaunchTemplates", "ec2:DescribeInstanceTypes", "ssm:DescribeInstanceInformation", "ssm:GetCommandInvocation"]
      Condition = { StringEquals = { "aws:RequestedRegion" = var.region } }
    },
    {
      Sid      = "ReadFoundationRoles", Effect = "Allow"
      Action   = ["iam:GetRole", "iam:ListRolePolicies", "iam:GetRolePolicy", "iam:ListAttachedRolePolicies"]
      Resource = [aws_iam_role.instance.arn, aws_iam_role.operator.arn]
    },
    { Sid = "ReadProfile", Effect = "Allow", Action = ["iam:GetInstanceProfile"], Resource = [aws_iam_instance_profile.devbox.arn] },
    {
      Sid       = "PinnedLaunchDependencies", Effect = "Allow", Action = ["ec2:RunInstances"]
      Resource  = ["arn:aws:ec2:${var.region}::image/${var.ami_id}", aws_subnet.devbox.arn, aws_security_group.devbox.arn, aws_launch_template.agent.arn]
      Condition = local.launch_condition
    },
    {
      Sid       = "TemplateNetworkInterface", Effect = "Allow", Action = ["ec2:RunInstances"], Resource = ["${local.ec2}:network-interface/*"]
      Condition = merge(local.launch_condition, { Bool = { "ec2:IsLaunchTemplateResource" = "true" } })
    },
    {
      Sid = "TaggedInstances", Effect = "Allow", Action = ["ec2:RunInstances"], Resource = ["${local.ec2}:instance/*"]
      Condition = {
        ArnEquals = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn }
        StringEquals = merge(local.request_scope, {
          "aws:RequestedRegion" = var.region, "aws:RequestTag/Profile" = "agent", "ec2:MetadataHttpTokens" = "required",
          "ec2:InstanceProfile" = aws_iam_instance_profile.devbox.arn, "ec2:InstanceMarketType" = "on-demand"
        })
        StringLike                  = { "aws:RequestTag/Name" = "?*", "aws:RequestTag/RequestId" = "?*", "aws:RequestTag/CreatedAt" = "?*" }
        "ForAllValues:StringEquals" = { "aws:TagKeys" = local.creation_tags }
      }
    },
    {
      Sid = "TaggedEncryptedVolumes", Effect = "Allow", Action = ["ec2:RunInstances"], Resource = ["${local.ec2}:volume/*"]
      Condition = {
        ArnEquals                   = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn }
        StringEquals                = merge(local.request_scope, { "aws:RequestedRegion" = var.region, "aws:RequestTag/Profile" = "agent" })
        StringLike                  = { "aws:RequestTag/Name" = "?*", "aws:RequestTag/RequestId" = "?*", "aws:RequestTag/CreatedAt" = "?*" }
        Bool                        = { "ec2:Encrypted" = "true" }
        "ForAllValues:StringEquals" = { "aws:TagKeys" = local.creation_tags }
      }
    },
    {
      Sid = "TagOnlyAtLaunch", Effect = "Allow", Action = ["ec2:CreateTags"], Resource = ["${local.ec2}:instance/*", "${local.ec2}:volume/*"]
      Condition = {
        StringEquals                = merge(local.request_scope, { "ec2:CreateAction" = "RunInstances", "aws:RequestedRegion" = var.region, "aws:RequestTag/Profile" = "agent" })
        "ForAllValues:StringEquals" = { "aws:TagKeys" = local.creation_tags }
      }
    },
    {
      Sid       = "PassWorkerRole", Effect = "Allow", Action = ["iam:PassRole"], Resource = [aws_iam_role.instance.arn]
      Condition = { StringEquals = { "iam:PassedToService" = "ec2.amazonaws.com" } }
    },
    {
      Sid       = "TerminateOwnedInstances", Effect = "Allow", Action = ["ec2:TerminateInstances"], Resource = ["${local.ec2}:instance/*"]
      Condition = { StringEquals = merge(local.resource_scope, { "aws:RequestedRegion" = var.region }) }
    },
    {
      Sid = "SSHOwnedInstances", Effect = "Allow", Action = ["ssm:StartSession"], Resource = ["${local.ec2}:instance/*"]
      Condition = {
        StringEquals = merge(local.ssm_scope, { "aws:RequestedRegion" = var.region })
        Bool         = { "ssm:SessionDocumentAccessCheck" = "true" }
      }
    },
    { Sid = "SSHDocument", Effect = "Allow", Action = ["ssm:StartSession"], Resource = ["arn:aws:ssm:${var.region}::document/AWS-StartSSHSession"] },
    {
      Sid       = "ReadinessOwnedInstances", Effect = "Allow", Action = ["ssm:SendCommand"], Resource = ["${local.ec2}:instance/*"]
      Condition = { StringEquals = merge(local.ssm_scope, { "aws:RequestedRegion" = var.region }) }
    },
    {
      Sid      = "CloseOwnSessions", Effect = "Allow", Action = ["ssm:TerminateSession", "ssm:ResumeSession"]
      Resource = ["arn:aws:ssm:${var.region}:${var.account_id}:session/${local.name}-*"]
    },
    { Sid = "ReadinessDocument", Effect = "Allow", Action = ["ssm:SendCommand", "ssm:GetDocument"], Resource = [aws_ssm_document.readiness.arn] }
  ] })
}
resource "aws_iam_role" "instance" {
  name               = "${local.name}-instance"
  assume_role_policy = local.instance_trust
  tags               = local.tags
}
resource "aws_iam_role_policy" "instance" {
  name   = "devbox-instance"
  role   = aws_iam_role.instance.id
  policy = local.instance_policy
}
resource "aws_iam_instance_profile" "devbox" {
  name = local.name
  role = aws_iam_role.instance.name
  tags = local.tags
}
resource "aws_iam_role" "operator" {
  name                 = "${local.name}-operator"
  assume_role_policy   = local.operator_trust
  max_session_duration = 3600
  tags                 = local.tags
}
resource "aws_iam_role_policy" "operator" {
  name   = "devbox-operator"
  role   = aws_iam_role.operator.id
  policy = local.operator_policy
}
