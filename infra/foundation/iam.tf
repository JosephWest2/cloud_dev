locals {
  instance_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole"
  }] })
  operator_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect    = "Allow", Principal = { AWS = var.operator_principal_arn }, Action = "sts:AssumeRole"
    Condition = { StringEquals = { "sts:RoleSessionName" = local.name } }
  }] })
  instance_policy = jsonencode({ Version = "2012-10-17", Statement = [
    {
      Sid       = "AgentTransport", Effect = "Allow"
      Action    = ["ssm:UpdateInstanceInformation", "ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel", "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel"]
      Resource  = "*"
      Condition = { StringEquals = { "aws:RequestedRegion" = var.region } }
    },
    { Sid = "ReadExecutionRecords", Effect = "Allow", Action = ["s3:GetObject"], Resource = local.result_object_arns },
    { Sid = "PublishExecutionResults", Effect = "Allow", Action = ["s3:PutObject"], Resource = local.worker_write_arns },
    { Sid = "ReadPinnedRunner", Effect = "Allow", Action = ["s3:GetObject"], Resource = ["${local.results_arn}/${local.runner_key}"] },
    {
      Sid       = "ListOwnedResults", Effect = "Allow", Action = ["s3:ListBucket"], Resource = [local.results_arn]
      Condition = { StringLike = { "s3:prefix" = "${local.results_prefix}*" } }
    },
  ] })
  request_scope  = { for k, v in local.tags : "aws:RequestTag/${k}" => v }
  resource_scope = { for k, v in local.tags : "ec2:ResourceTag/${k}" => v }
  ssm_scope      = { for k, v in local.tags : "ssm:resourceTag/${k}" => v }
  creation_tags  = ["ManagedBy", "Deployment", "Owner", "Profile", "Name", "BaseName", "NamingVersion", "RequestId", "BatchId", "AttemptId", "CreatedAt", "Group"]
  subnet_arns    = [for id in local.subnet_ids : "${local.ec2}:subnet/${id}"]
  creation_scope = merge(local.request_scope, {
    "aws:RequestedRegion" = var.region, "aws:RequestTag/Profile" = "agent", "aws:RequestTag/NamingVersion" = "1"
  })
  dynamic_creation_tags = { for key in ["Name", "BaseName", "RequestId", "BatchId", "AttemptId", "CreatedAt"] : "aws:RequestTag/${key}" => "?*" }
  creation_condition = {
    StringEquals                = local.creation_scope
    StringLike                  = local.dynamic_creation_tags
    "ForAllValues:StringEquals" = { "aws:TagKeys" = local.creation_tags }
  }
  # Every created resource must be tagged, which makes EC2 authorize CreateTags
  # as part of creation. The tag grants enforce the complete scope, identity
  # and key allowlist once, staying within IAM's aggregate inline-policy quota.
  # Without this required tag, untagged creation would bypass CreateTags checks.
  tagged_creation_condition = {
    StringEquals = { "aws:RequestedRegion" = var.region, "aws:RequestTag/ManagedBy" = "devbox" }
  }
  launch_condition = {
    ArnEquals    = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn }
    StringEquals = { "aws:RequestedRegion" = var.region }
  }
  operator_policy = jsonencode({ Version = "2012-10-17", Statement = [
    {
      Sid       = "RegionalInventory", Effect = "Allow", Resource = "*"
      Action    = ["ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:DescribeVpcs", "ec2:DescribeVpcAttribute", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups", "ec2:DescribeRouteTables", "ec2:DescribeInternetGateways", "ec2:DescribeImages", "ec2:DescribeLaunchTemplateVersions", "ec2:DescribeLaunchTemplates", "ec2:DescribeInstanceTypes", "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeAvailabilityZones", "ec2:DescribeFleets", "ssm:DescribeInstanceInformation", "ssm:GetCommandInvocation"]
      Condition = { StringEquals = { "aws:RequestedRegion" = var.region } }
    },
    {
      Sid      = "ReadRoles", Effect = "Allow"
      Action   = ["iam:GetRole", "iam:ListRolePolicies", "iam:GetRolePolicy", "iam:ListAttachedRolePolicies"]
      Resource = [aws_iam_role.instance.arn, aws_iam_role.operator.arn]
    },
    { Sid = "ReadProfile", Effect = "Allow", Action = ["iam:GetInstanceProfile"], Resource = [aws_iam_instance_profile.devbox.arn] },
    { Sid = "ReadSpotRole", Effect = "Allow", Action = ["iam:GetRole"], Resource = ["arn:aws:iam::${var.account_id}:role/aws-service-role/spot.amazonaws.com/AWSServiceRoleForEC2Spot"] },
    {
      Sid       = "TaggedFleet", Effect = "Allow", Action = ["ec2:CreateFleet"], Resource = ["${local.ec2}:fleet/*"]
      Condition = local.tagged_creation_condition
    },
    {
      Sid       = "FleetInstances", Effect = "Allow", Action = ["ec2:CreateFleet"], Resource = ["${local.ec2}:instance/*"]
      Condition = merge(local.tagged_creation_condition, { StringEquals = merge(local.tagged_creation_condition.StringEquals, { "ec2:InstanceProfile" = aws_iam_instance_profile.devbox.arn }) })
    },
    {
      # CreateFleet omits ec2:LaunchTemplate. Every RunInstances request must
      # also satisfy TaggedInstances/NIC, which require it without IfExists.
      Sid       = "LaunchDependencies", Effect = "Allow", Action = ["ec2:RunInstances", "ec2:CreateFleet"]
      Resource  = concat(["arn:aws:ec2:${var.region}::image/${var.ami_id}", aws_security_group.devbox.arn, aws_launch_template.agent.arn], local.subnet_arns)
      Condition = { ArnEqualsIfExists = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn }, StringEquals = { "aws:RequestedRegion" = var.region } }
    },
    {
      Sid = "LaunchNIC", Effect = "Allow", Action = ["ec2:RunInstances"], Resource = ["${local.ec2}:network-interface/*"]
      Condition = merge(local.launch_condition, {
        ArnEquals = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn, "ec2:Subnet" = local.subnet_arns }
        Bool      = { "ec2:AssociatePublicIpAddress" = "true" }
      })
    },
    {
      Sid = "TaggedInstances", Effect = "Allow", Action = ["ec2:RunInstances"], Resource = ["${local.ec2}:instance/*"]
      Condition = merge(local.tagged_creation_condition, {
        ArnEquals = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn }
        StringEquals = merge(local.tagged_creation_condition.StringEquals, {
          "ec2:MetadataHttpTokens" = "required", "ec2:InstanceProfile" = aws_iam_instance_profile.devbox.arn,
          "ec2:InstanceMarketType" = ["spot", "on-demand"]
        })
      })
    },
    {
      Sid = "EncryptedVolumes", Effect = "Allow", Action = ["ec2:RunInstances", "ec2:CreateFleet"], Resource = ["${local.ec2}:volume/*"]
      Condition = merge(local.tagged_creation_condition, {
        ArnEqualsIfExists = { "ec2:LaunchTemplate" = aws_launch_template.agent.arn }
        StringEquals      = merge(local.tagged_creation_condition.StringEquals, { "ec2:VolumeType" = "gp3" })
        Bool              = { "ec2:Encrypted" = "true" }
      })
    },
    {
      # Retain seven-tag RunInstances recovery for legacy prepared receipts.
      Sid = "TagOnlyAtLaunch", Effect = "Allow", Action = ["ec2:CreateTags"], Resource = ["${local.ec2}:instance/*", "${local.ec2}:volume/*"]
      Condition = {
        StringEquals                = merge(local.request_scope, { "aws:RequestedRegion" = var.region, "aws:RequestTag/Profile" = "agent", "ec2:CreateAction" = "RunInstances" })
        StringLike                  = { "aws:RequestTag/Name" = "?*", "aws:RequestTag/RequestId" = "?*", "aws:RequestTag/CreatedAt" = "?*" }
        "ForAllValues:StringEquals" = { "aws:TagKeys" = local.creation_tags }
      }
    },
    {
      Sid = "TagFleetAtLaunch", Effect = "Allow", Action = ["ec2:CreateTags"], Resource = ["${local.ec2}:fleet/*", "${local.ec2}:instance/*", "${local.ec2}:volume/*"]
      Condition = merge(local.creation_condition, {
        StringEquals = merge(local.creation_scope, { "ec2:CreateAction" = "CreateFleet" })
      })
    },
    {
      Sid       = "PassWorkerRole", Effect = "Allow", Action = ["iam:PassRole"], Resource = [aws_iam_role.instance.arn]
      Condition = { StringEquals = { "iam:PassedToService" = "ec2.amazonaws.com" } }
    },
    {
      Sid       = "TerminateOwned", Effect = "Allow", Action = ["ec2:TerminateInstances"], Resource = ["${local.ec2}:instance/*"]
      Condition = { StringEquals = merge(local.resource_scope, { "aws:RequestedRegion" = var.region }) }
    },
    {
      Sid = "SSHOwnedInstances", Effect = "Allow", Action = ["ssm:StartSession"], Resource = ["${local.ec2}:instance/*"]
      Condition = {
        StringEquals = merge(local.ssm_scope, { "aws:RequestedRegion" = var.region })
        # Explicit session documents can omit this context key. Keep the check
        # for default-document requests, and authorize only SSHDocument below.
        BoolIfExists = { "ssm:SessionDocumentAccessCheck" = "true" }
      }
    },
    { Sid = "SSHDocument", Effect = "Allow", Action = ["ssm:StartSession"], Resource = ["arn:aws:ssm:${var.region}::document/AWS-StartSSHSession"] },
    {
      Sid       = "ProbeOwned", Effect = "Allow", Action = ["ssm:SendCommand"], Resource = ["${local.ec2}:instance/*"]
      Condition = { StringEquals = merge(local.ssm_scope, { "aws:RequestedRegion" = var.region }) }
    },
    {
      Sid      = "OwnSessions", Effect = "Allow", Action = ["ssm:TerminateSession", "ssm:ResumeSession", "ssmmessages:OpenDataChannel"]
      Resource = ["arn:aws:ssm:${var.region}:${var.account_id}:session/${local.name}-*"]
    },
    { Sid = "ReadinessDocument", Effect = "Allow", Action = ["ssm:SendCommand", "ssm:GetDocument"], Resource = [aws_ssm_document.readiness.arn] },
    { Sid = "ExecutionDocument", Effect = "Allow", Action = ["ssm:SendCommand", "ssm:GetDocument"], Resource = [aws_ssm_document.execution.arn] },
    { Sid = "ReadResults", Effect = "Allow", Action = ["s3:GetObject"], Resource = local.result_object_arns },
    { Sid = "PrepareExecution", Effect = "Allow", Action = ["s3:PutObject"], Resource = local.operator_write_arns },
    { Sid = "LaunchRecords", Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject"], Resource = [local.launch_ledger_arn] },
    { Sid = "ReadPinnedRunner", Effect = "Allow", Action = ["s3:GetObject"], Resource = ["${local.results_arn}/${local.runner_key}"] },
    {
      Sid       = "ListOwnedResults", Effect = "Allow", Action = ["s3:ListBucket"], Resource = [local.results_arn]
      Condition = { StringLike = { "s3:prefix" = ["${local.results_prefix}*", "${local.launch_ledger_prefix}*"] } }
    },
    {
      Sid    = "ReadBucketConfig", Effect = "Allow", Resource = [local.results_arn]
      Action = ["s3:GetBucketLocation", "s3:GetBucketPolicy", "s3:GetBucketPublicAccessBlock", "s3:GetBucketOwnershipControls", "s3:GetEncryptionConfiguration", "s3:GetBucketVersioning", "s3:GetLifecycleConfiguration", "s3:GetBucketTagging"]
    },
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
  lifecycle {
    precondition {
      condition     = length(local.instance_policy) <= 10240
      error_message = "Instance inline policy exceeds IAM's 10240-byte aggregate quota; review policy compaction before applying."
    }
  }
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
  lifecycle {
    precondition {
      condition     = length(local.operator_policy) <= 10240
      error_message = "Operator inline policy exceeds IAM's 10240-byte aggregate quota; shorten deployment/owner labels or review policy compaction before applying."
    }
  }
}
