mock_provider "aws" {
  mock_data "aws_availability_zones" { defaults = { names = ["us-east-2a", "us-east-2b", "us-east-2c"] } }
  mock_data "aws_ec2_instance_type" {
    defaults = {
      supported_architectures  = ["x86_64"], supported_virtualization_types = ["hvm"], supported_root_device_types = ["ebs"]
      ebs_encryption_support   = "supported", ena_support = "required", maximum_network_interfaces = 4, maximum_ipv4_addresses_per_interface = 15
      supported_usages_classes = ["spot", "on-demand"]
    }
  }
  mock_data "aws_ec2_instance_type_offerings" { defaults = { instance_types = ["c7i.2xlarge", "c7a.2xlarge", "c6i.2xlarge", "c6a.2xlarge"] } }
  mock_resource "aws_vpc" { defaults = { id = "vpc-0123456789abcdef0" } }
  mock_resource "aws_route_table" { defaults = { id = "rtb-0123456789abcdef0" } }
  mock_resource "aws_internet_gateway" { defaults = { id = "igw-0123456789abcdef0" } }

  mock_resource "aws_iam_instance_profile" { defaults = { arn = "arn:aws:iam::123456789012:instance-profile/devbox-test-test-owner" } }
  mock_resource "aws_iam_role" { defaults = { arn = "arn:aws:iam::123456789012:role/devbox-test-test-owner-instance" } }
  mock_resource "aws_subnet" { defaults = { id = "subnet-0123456789abcdef0", arn = "arn:aws:ec2:us-east-2:123456789012:subnet/subnet-0123456789abcdef0" } }
  mock_resource "aws_security_group" { defaults = { id = "sg-0123456789abcdef0", arn = "arn:aws:ec2:us-east-2:123456789012:security-group/sg-0123456789abcdef0" } }
  mock_resource "aws_launch_template" { defaults = { id = "lt-0123456789abcdef0", arn = "arn:aws:ec2:us-east-2:123456789012:launch-template/lt-0123456789abcdef0", latest_version = 1 } }
  mock_resource "aws_ssm_document" { defaults = { arn = "arn:aws:ssm:us-east-2:123456789012:document/devbox-test-test-owner-readiness", latest_version = "1" } }

  mock_data "aws_ami" {
    defaults = {
      id                  = "ami-0123456789abcdef0"
      architecture        = "x86_64"
      virtualization_type = "hvm"
      root_device_type    = "ebs"
      root_device_name    = "/dev/sda1"
      name                = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260901"
      block_device_mappings = [{
        device_name  = "/dev/sda1"
        no_device    = ""
        virtual_name = ""
        ebs          = { volume_size = 8, snapshot_id = "snap-0123456789abcdef0", delete_on_termination = true, encrypted = false, iops = 0, throughput = 0, volume_type = "gp3" }
      }]
    }
  }
}
override_resource {
  target = aws_subnet.additional
  values = { id = "subnet-0123456789abcdef1", arn = "arn:aws:ec2:us-east-2:123456789012:subnet/subnet-0123456789abcdef1" }
}
override_resource {
  target = aws_iam_role.operator
  values = { arn = "arn:aws:iam::123456789012:role/devbox-test-test-owner-operator" }
}
override_resource {
  target = aws_ssm_document.execution
  values = { arn = "arn:aws:ssm:us-east-2:123456789012:document/devbox-test-test-owner-execute", latest_version = "1" }
}
variables {
  # OpenTofu mock overrides apply to whole resources, not for_each instances.
  # Two AZs permit distinct mocked subnet IDs for the real Go export bridge.
  availability_zones     = ["us-east-2a", "us-east-2b"]
  ssh_public_key         = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
  account_id             = "123456789012"
  deployment             = "test"
  owner                  = "test-owner"
  ami_id                 = "ami-0123456789abcdef0"
  operator_principal_arn = "arn:aws:iam::123456789012:role/TestLogin"
}
# Static authorization contracts. Mock contexts do not establish AWS effective
# permissions; restricted-operator launch acceptance belongs to issue #34.
run "spot_policy_boundary" {
  command = apply
  assert {
    condition = (toset([for s in jsondecode(local.operator_policy).Statement : s.Sid if contains(s.Action, "ec2:CreateFleet")]) == toset(["LaunchDependencies", "TaggedFleet", "FleetResources"])
    )
    error_message = "Fleet authorization must separate existing dependencies, the tagged fleet, and preliminary instance/volume checks."
  }
  assert {
    condition = (alltrue([for sid in ["TaggedFleet", "TaggedInstances", "EncryptedVolumes"] :
      length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == sid && s.Condition.StringEquals["aws:RequestedRegion"] == "us-east-2" && s.Condition.StringEquals["aws:RequestTag/ManagedBy"] == "devbox"]) == 1
    ]))
    error_message = "Every created fleet, instance and volume must have ManagedBy, forcing dependent CreateTags authorization; untagged requests cannot bypass it."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "TagFleetAtLaunch" &&
      s.Condition.StringEquals["aws:RequestedRegion"] == "us-east-2" && s.Condition.StringEquals["aws:RequestTag/ManagedBy"] == "devbox" &&
      s.Condition.StringEquals["aws:RequestTag/Deployment"] == "test" && s.Condition.StringEquals["aws:RequestTag/Owner"] == "test-owner" &&
      s.Condition.StringEquals["aws:RequestTag/Profile"] == "agent" && s.Condition.StringEquals["aws:RequestTag/NamingVersion"] == "1" &&
      toset(s.Condition["ForAllValues:StringEquals"]["aws:TagKeys"]) == toset(["ManagedBy", "Deployment", "Owner", "Profile", "Name", "BaseName", "NamingVersion", "RequestId", "BatchId", "AttemptId", "CreatedAt", "Group"]) &&
      alltrue([for key in ["Name", "BaseName", "RequestId", "BatchId", "AttemptId", "CreatedAt"] : s.Condition.StringLike["aws:RequestTag/${key}"] == "?*"])
    ]) == 1)
    error_message = "Dependent Fleet tag authorization must enforce all scope and batch identity tags while allowing optional Group."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Action == ["ec2:RunInstances"] &&
      s.Resource == ["arn:aws:ec2:us-east-2:123456789012:volume/*"] && s.Condition.Bool["ec2:Encrypted"] == "true" &&
      s.Condition.StringEquals["ec2:VolumeType"] == "gp3" && !can(s.Condition.StringEquals["ec2:InstanceProfile"]) && !can(s.Condition.StringEquals["ec2:MetadataHttpTokens"])
      ]) == 1
    )
    error_message = "The dependent RunInstances volume check must require encryption and gp3 without instance-only conditions."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "TaggedInstances" && s.Action == ["ec2:RunInstances"] &&
      s.Resource == ["arn:aws:ec2:us-east-2:123456789012:instance/*"] && s.Condition.StringEquals["ec2:InstanceProfile"] == aws_iam_instance_profile.devbox.arn &&
      toset(s.Condition.StringEquals["ec2:InstanceMarketType"]) == toset(["spot", "on-demand"]) && s.Condition.StringEquals["ec2:MetadataHttpTokens"] == "required" && s.Condition.ArnEquals["ec2:LaunchTemplate"] == aws_launch_template.agent.arn
      ]) == 1
    )
    error_message = "RunInstances must retain the approved profile, IMDSv2 and exact template for both markets."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "FleetResources" &&
      s.Action == ["ec2:CreateFleet"] && toset(s.Resource) == toset(["arn:aws:ec2:us-east-2:123456789012:instance/*", "arn:aws:ec2:us-east-2:123456789012:volume/*"]) &&
      s.Condition == { StringEquals = { "aws:RequestedRegion" = "us-east-2" } }
    ]) == 1)
    error_message = "Preliminary Fleet instance/volume authorization has no launch-property context and must never grant RunInstances."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "LaunchDependencies" &&
      toset(s.Resource) == toset(concat(["arn:aws:ec2:us-east-2::image/ami-0123456789abcdef0", aws_launch_template.agent.arn, aws_security_group.devbox.arn], [for id in local.subnet_ids : "arn:aws:ec2:us-east-2:123456789012:subnet/${id}"])) &&
      s.Condition.ArnEqualsIfExists["ec2:LaunchTemplate"] == aws_launch_template.agent.arn && toset(s.Action) == toset(["ec2:CreateFleet", "ec2:RunInstances"])
      ]) == 1
    )
    error_message = "Foreign AMIs, templates, security groups and subnets must have no launch dependency grant."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "LaunchNIC" &&
      s.Resource == ["arn:aws:ec2:us-east-2:123456789012:network-interface/*"] &&
      toset(s.Condition.ArnEquals["ec2:Subnet"]) == toset([for id in local.subnet_ids : "arn:aws:ec2:us-east-2:123456789012:subnet/${id}"]) &&
      s.Condition.ArnEquals["ec2:LaunchTemplate"] == aws_launch_template.agent.arn && s.Condition.Bool["ec2:AssociatePublicIpAddress"] == "true"
      ]) == 1
    )
    error_message = "NIC creation must retain the approved subnets and template with public IPv4 after removing the template's embedded subnet."
  }
  assert {
    condition = (alltrue([for s in jsondecode(local.operator_policy).Statement :
      !can(s.Condition.StringEquals["ec2:FleetType"]) && !can(s.Condition.ArnEquals["ec2:LaunchTemplate"]) && !can(s.Condition.StringEquals["ec2:MetadataHttpTokens"]) && !can(s.Condition.StringEquals["ec2:InstanceMarketType"])
      if contains(s.Action, "ec2:CreateFleet")
      ])
    )
    error_message = "Do not deny every Fleet request using condition keys absent from the CreateFleet authorization model."
  }
  assert {
    condition = (toset([for s in jsondecode(local.operator_policy).Statement : s.Sid if contains(s.Action, "ec2:CreateTags")]) == toset(["TagOnlyAtLaunch", "TagFleetAtLaunch"]) &&
      length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "TagOnlyAtLaunch" && s.Condition.StringEquals["ec2:CreateAction"] == "RunInstances" && toset(s.Resource) == toset(["arn:aws:ec2:us-east-2:123456789012:instance/*", "arn:aws:ec2:us-east-2:123456789012:volume/*"])]) == 1 &&
      length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "TagFleetAtLaunch" && s.Condition.StringEquals["ec2:CreateAction"] == "CreateFleet" && toset(s.Resource) == toset(["arn:aws:ec2:us-east-2:123456789012:fleet/*", "arn:aws:ec2:us-east-2:123456789012:instance/*", "arn:aws:ec2:us-east-2:123456789012:volume/*"])]) == 1
    )
    error_message = "Tag permissions must apply only during each supported resource-creation API, never to retag existing resources."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "PassWorkerRole" && s.Resource == [aws_iam_role.instance.arn] && s.Condition.StringEquals["iam:PassedToService"] == "ec2.amazonaws.com"]) == 1 &&
      length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "ReadSpotRole" && s.Action == ["iam:GetRole"] && s.Resource == ["arn:aws:iam::123456789012:role/aws-service-role/spot.amazonaws.com/AWSServiceRoleForEC2Spot"]]) == 1 &&
      alltrue([for s in jsondecode(local.operator_policy).Statement : !contains(s.Action, "iam:CreateServiceLinkedRole") && !contains(s.Action, "iam:DeleteServiceLinkedRole") && !contains(s.Action, "ec2:ModifyFleet") && !contains(s.Action, "ec2:RequestSpotFleet") && !contains(s.Action, "ec2:RequestSpotInstances") && !contains(s.Action, "ec2:DescribeFleetInstances")])
    )
    error_message = "Only the worker role may be passed, Spot role setup stays outside runtime, and instant Fleet does not need legacy allocation/maintenance APIs."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "RegionalInventory" && contains(s.Action, "ec2:DescribeFleets") && contains(s.Action, "ec2:DescribeInstanceTypeOfferings") && contains(s.Action, "ec2:DescribeAvailabilityZones") && s.Condition.StringEquals["aws:RequestedRegion"] == "us-east-2"]) == 1
    )
    error_message = "Offerings and instant-Fleet reconciliation require regional read APIs."
  }
  assert {
    condition = (length(local.operator_policy) <= 10240 && length(local.instance_policy) <= 10240
    )
    error_message = "Rendered IAM role inline policies must fit their aggregate quota."
  }
}

run "permanent_launch_ledger" {
  command = apply
  assert {
    condition = (local.launch_ledger_manifest.schema_version == 1 && local.launch_ledger_manifest.bucket == local.results_manifest.bucket &&
      local.launch_ledger_manifest.expected_bucket_owner == "123456789012" && local.launch_ledger_manifest.region == "us-east-2" &&
      local.launch_ledger_manifest.prefix == "launches/v2/123456789012/us-east-2/test/test-owner/" && local.launch_ledger_manifest.policy_sha256 == local.results_manifest.policy_sha256
    )
    error_message = "The launch ledger must share the private result bucket while pinning its separate exact scope and reviewed bucket policy."
  }
  assert {
    condition = (length([for s in jsondecode(local.results_policy).Statement : s if s.Sid == "RequireImmutableLaunchCreation" &&
      s.Effect == "Deny" && s.Principal == "*" && s.Action == "s3:PutObject" && s.Resource == "${local.results_arn}/launches/v2/123456789012/us-east-2/test/test-owner/*" &&
      s.Condition == { StringNotEquals = { "s3:if-none-match" = "*" } }
      ]) == 1
    )
    error_message = "Every ledger writer must use If-None-Match: *, including setup identities; missing/wrong headers and copies cannot overwrite claims."
  }
  assert {
    condition = (length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "LaunchRecords" &&
      toset(s.Action) == toset(["s3:GetObject", "s3:PutObject"]) && s.Resource == ["${local.results_arn}/launches/v2/123456789012/us-east-2/test/test-owner/*"]
      ]) == 1 && length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "ListOwnedResults" &&
      s.Action == ["s3:ListBucket"] && s.Resource == [local.results_arn] && toset(s.Condition.StringLike["s3:prefix"]) == toset(["results/v1/123456789012/us-east-2/test/test-owner/*", "launches/v2/123456789012/us-east-2/test/test-owner/*"])
      ]) == 1
    )
    error_message = "Operator ledger read/write/list access must stay under the exact owned prefix."
  }
  assert {
    condition = (alltrue([for s in jsondecode(local.instance_policy).Statement :
      alltrue([for a in s.Action : !startswith(a, "s3:")]) ||
      (s.Sid == "ReadExecutionRecords" && s.Resource == local.result_object_arns) ||
      (s.Sid == "PublishExecutionResults" && s.Resource == local.worker_write_arns) ||
      (s.Sid == "ReadPinnedRunner" && s.Resource == ["${local.results_arn}/${local.runner_key}"]) ||
      (s.Sid == "ListOwnedResults" && s.Resource == [local.results_arn] && try(s.Condition.StringLike["s3:prefix"], "") == "${local.results_prefix}*")
      ]) && alltrue([for s in concat(jsondecode(local.instance_policy).Statement, jsondecode(local.operator_policy).Statement) :
      !contains(s.Action, "s3:DeleteObject") && !contains(s.Action, "s3:DeleteObjectVersion") && !contains(s.Action, "s3:*") && !contains(s.Action, "*")
      ])
    )
    error_message = "Workers must have no ledger access and runtime roles must never erase dispatch claims."
  }
  assert {
    condition = (length(aws_s3_bucket_lifecycle_configuration.results.rule) == 1 &&
      one(one(aws_s3_bucket_lifecycle_configuration.results.rule).filter).prefix == "results/v1/123456789012/us-east-2/test/test-owner/" &&
      one(one(aws_s3_bucket_lifecycle_configuration.results.rule).expiration).days == 30 && !aws_s3_bucket.results.force_destroy
    )
    error_message = "Result expiration must not age out launch records, and foundation destroy must not silently empty the ledger."
  }
}

# Match the live deployment's label and ARN lengths across all three AZs. The
# mock provider gives b/c the same ID; repeated ARN entries still count toward
# the quota exactly as two distinct IDs of the same length would.
run "three_az_live_scope_policy_quota" {
  command = plan
  variables {
    availability_zones = ["us-east-2a", "us-east-2b", "us-east-2c"]
    deployment         = "personal-dev"
    owner              = "joseph"
  }
  override_resource {
    target = aws_iam_instance_profile.devbox
    values = { arn = "arn:aws:iam::123456789012:instance-profile/devbox-personal-dev-joseph" }
  }
  override_resource {
    target = aws_iam_role.instance
    values = { arn = "arn:aws:iam::123456789012:role/devbox-personal-dev-joseph-instance" }
  }
  override_resource {
    target = aws_iam_role.operator
    values = { arn = "arn:aws:iam::123456789012:role/devbox-personal-dev-joseph-operator" }
  }
  override_resource {
    target = aws_ssm_document.readiness
    values = { arn = "arn:aws:ssm:us-east-2:123456789012:document/devbox-personal-dev-joseph-readiness", latest_version = "1" }
  }
  override_resource {
    target = aws_ssm_document.execution
    values = { arn = "arn:aws:ssm:us-east-2:123456789012:document/devbox-personal-dev-joseph-execute", latest_version = "1" }
  }
  assert {
    condition     = length(local.operator_policy) <= 10240 && length(local.operator_policy) > 10000
    error_message = "The full three-AZ live scope must fit IAM's inline-role quota, including repeated subnet and scoped ARNs."
  }
}

# IAM counts repeated scoped ARNs too. Valid label grammar is not a promise that
# every resulting inline policy fits; fail before an oversized policy is sent.
run "oversized_policy_rejected" {
  command = plan
  variables {
    deployment = "ddddddddddddddddddddddd"
    owner      = "ooooooooooooooooooooooo"
  }
  expect_failures = [aws_iam_role_policy.operator]
}
