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
run "safe_foundation_plan" {
  command = plan
  assert {
    condition     = length(aws_security_group.devbox.ingress) == 0 && length(aws_security_group.devbox.egress) == 2
    error_message = "Workers must have no ingress and only HTTP/HTTPS egress."
  }
  assert {
    condition     = one(one(aws_launch_template.agent.block_device_mappings).ebs).encrypted && one(one(aws_launch_template.agent.block_device_mappings).ebs).delete_on_termination
    error_message = "Root volumes must be encrypted and deleted on termination."
  }
  assert {
    condition     = one(aws_launch_template.agent.metadata_options).http_tokens == "required" && one(aws_launch_template.agent.metadata_options).http_put_response_hop_limit == 1
    error_message = "IMDSv2 is required with one hop."
  }
  assert {
    condition     = one(aws_launch_template.agent.network_interfaces).associate_public_ip_address && !aws_subnet.devbox.map_public_ip_on_launch
    error_message = "Only worker template launches allocate public IPs."
  }
  assert {
    condition     = aws_launch_template.agent.image_id == var.ami_id
    error_message = "Pin the selected AMI."
  }
  assert {
    condition     = !can(jsondecode(aws_ssm_document.readiness.content).parameters) && length(jsondecode(aws_ssm_document.readiness.content).mainSteps) == 1
    error_message = "Readiness is a fixed probe without command parameters."
  }
}
run "wrong_region" {
  command = plan
  variables { region = "us-west-2" }
  expect_failures = [var.region]
}

run "three_zone_placement" {
  command = plan
  variables { availability_zones = ["us-east-2c", "us-east-2a", "us-east-2b"] }
  assert {
    condition     = aws_subnet.devbox.cidr_block == "10.77.1.0/24" && aws_subnet.devbox.availability_zone == "us-east-2a" && aws_subnet.additional["us-east-2b"].cidr_block == "10.77.2.0/24" && aws_subnet.additional["us-east-2c"].cidr_block == "10.77.3.0/24"
    error_message = "AZ order must not renumber or overlap subnets; preserve original resource and CIDR."
  }
  assert {
    condition     = length(aws_route_table_association.additional) == 2 && alltrue([for a in aws_route_table_association.additional : a.route_table_id == aws_route_table.devbox.id]) && one(aws_launch_template.agent.network_interfaces).subnet_id == null && alltrue([for s in aws_subnet.additional : !s.map_public_ip_on_launch])
    error_message = "Associate every selected subnet while leaving Fleet subnet selection outside the pinned public-IP interface."
  }
}
run "selected_zones_without_original" {
  command = plan
  variables { availability_zones = ["us-east-2b"] }
  assert {
    condition     = aws_subnet.devbox.cidr_block == "10.77.1.0/24" && length(local.selected_subnets) == 1 && local.selected_subnets[0].availability_zone == "us-east-2b"
    error_message = "Excluding the original AZ from launches must not remove its durable subnet."
  }
}
run "duplicate_zones" {
  command = plan
  variables { availability_zones = ["us-east-2a", "us-east-2a"] }
  expect_failures = [var.availability_zones]
}
run "empty_zones" {
  command = plan
  variables { availability_zones = [] }
  expect_failures = [var.availability_zones]
}
run "foreign_zone" {
  command = plan
  variables { availability_zones = ["us-west-2a"] }
  expect_failures = [var.availability_zones]
}
run "duplicate_instance_type" {
  command = plan
  variables { instance_types = ["c7i.2xlarge", "c7i.2xlarge"] }
  expect_failures = [var.instance_types]
}
run "unsupported_type_architecture" {
  command = plan
  override_data {
    target = data.aws_ec2_instance_type.selected
    values = {
      supported_architectures  = ["arm64"], supported_virtualization_types = ["hvm"], supported_root_device_types = ["ebs"]
      ebs_encryption_support   = "supported", ena_support = "required", maximum_network_interfaces = 4, maximum_ipv4_addresses_per_interface = 15
      supported_usages_classes = ["spot", "on-demand"]
    }
  }
  expect_failures = [data.aws_ec2_instance_type.selected]
}
run "no_type_offerings" {
  command = plan
  override_data {
    target = data.aws_ec2_instance_type_offerings.selected
    values = { instance_types = [] }
  }
  expect_failures = [aws_launch_template.agent]
}
run "root_smaller_than_snapshot" {
  command = plan
  override_data {
    target = data.aws_ami.ubuntu
    values = {
      id                    = "ami-0123456789abcdef0", architecture = "x86_64", virtualization_type = "hvm", root_device_type = "ebs", root_device_name = "/dev/sda1"
      name                  = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260901"
      block_device_mappings = [{ device_name = "/dev/sda1", no_device = "", virtual_name = "", ebs = { volume_size = 101, snapshot_id = "snap-0123456789abcdef0", delete_on_termination = true, encrypted = false, iops = 0, throughput = 0, volume_type = "gp3" } }]
    }
  }
  expect_failures = [aws_launch_template.agent]
}
run "invalid_template_root_size" {
  command = plan
  variables { root_disk_gb = 8.5 }
  expect_failures = [var.root_disk_gb]
}
run "wrong_image" {
  command = plan
  override_data {
    target = data.aws_ami.ubuntu
    values = {
      architecture        = "arm64"
      virtualization_type = "hvm"
      root_device_type    = "ebs"
      root_device_name    = "/dev/sda1"
      name                = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260901"
    }
  }
  expect_failures = [data.aws_ami.ubuntu]
}
run "policy_contract" {
  command = apply # Mock provider only; resolves resource ARNs for policy assertions.
  assert {
    condition = alltrue([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement :
      s.Resource == [aws_iam_role.instance.arn] && s.Condition.StringEquals["iam:PassedToService"] == "ec2.amazonaws.com" if s.Sid == "PassWorkerRole"
    ])
    error_message = "PassRole must target only the worker role and EC2."
  }
  assert {
    condition = alltrue([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement :
      s.Condition.StringEquals["aws:RequestTag/Owner"] == var.owner && s.Condition.StringEquals["aws:RequestTag/Deployment"] == var.deployment && s.Condition.StringLike["aws:RequestTag/RequestId"] == "?*"
      if s.Sid == "TagAtCreation"
    ])
    error_message = "Mandatory creation tagging must enforce scoped and dynamic tags on both instances and volumes."
  }
  assert {
    condition = alltrue([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement :
      s.Condition.StringEquals["ec2:ResourceTag/Owner"] == var.owner && s.Condition.StringEquals["ec2:ResourceTag/Deployment"] == var.deployment
      if s.Sid == "TerminateOwned"
    ])
    error_message = "Termination must require matching owner and deployment."
  }
  assert {
    condition = length([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement : s
      if s.Sid == "OwnSessions" && contains(s.Action, "ssmmessages:OpenDataChannel") && s.Resource == ["arn:aws:ssm:us-east-2:123456789012:session/devbox-test-test-owner-*"]
    ]) == 1
    error_message = "The operator needs signed data-channel access scoped to its own sessions."
  }
  assert {
    condition = length([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement : s
      if s.Sid == "SSHOwnedInstances" && try(s.Condition.BoolIfExists["ssm:SessionDocumentAccessCheck"], "false") == "true" && s.Resource == ["arn:aws:ec2:us-east-2:123456789012:instance/*"] && s.Condition.StringEquals["ssm:resourceTag/Owner"] == var.owner && s.Condition.StringEquals["ssm:resourceTag/Deployment"] == var.deployment && s.Condition.StringEquals["ssm:resourceTag/ManagedBy"] == "devbox" && s.Condition.StringEquals["aws:RequestedRegion"] == var.region
    ]) == 1
    error_message = "SSH must retain instance scope and the default-document check while accepting absent context for explicit documents."
  }
  assert {
    condition = length([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement : s
      if contains(s.Action, "ssm:StartSession")
      ]) == 2 && length([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement : s
      if s.Sid == "SSHDocument" && s.Action == ["ssm:StartSession"] && s.Resource == ["arn:aws:ssm:us-east-2::document/AWS-StartSSHSession"]
    ]) == 1
    error_message = "StartSession must authorize only scoped instances and the exact SSH session document."
  }
  assert {
    condition     = length(local.operator_policy) <= 10240 && length(local.instance_policy) <= 10240
    error_message = "Inline policies exceed IAM role quotas."
  }
}

run "durable_execution_contract" {
  command = apply # Mock provider only; validates rendered bootstrap/manifest.
  assert {
    condition     = output.deployment_manifest.schema_version == 6 && output.deployment_manifest.results == local.results_manifest && output.deployment_manifest.execution == local.execution_manifest && output.deployment_manifest.launch_ledger == local.launch_ledger_manifest
    error_message = "Manifest v5 must retain exact execution/storage contracts and add permanent launch records."
  }
  assert {
    condition     = !aws_s3_bucket.results.force_destroy && one(aws_s3_bucket_versioning.results.versioning_configuration).status == "Disabled"
    error_message = "Result storage must be unversioned and must not be emptied by destroy."
  }
  assert {
    condition     = aws_s3_bucket_public_access_block.results.block_public_acls && aws_s3_bucket_public_access_block.results.block_public_policy && aws_s3_bucket_public_access_block.results.ignore_public_acls && aws_s3_bucket_public_access_block.results.restrict_public_buckets && one(aws_s3_bucket_ownership_controls.results.rule).object_ownership == "BucketOwnerEnforced"
    error_message = "Result storage requires all public-access blocks and bucket-owner-enforced ownership."
  }
  assert {
    condition     = one(one(aws_s3_bucket_server_side_encryption_configuration.results.rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256"
    error_message = "Result storage must use SSE-S3 encryption."
  }
  assert {
    condition     = length(aws_s3_bucket_lifecycle_configuration.results.rule) == 1 && aws_s3_bucket_lifecycle_configuration.results.rule[0].status == "Enabled" && aws_s3_bucket_lifecycle_configuration.results.rule[0].filter[0].prefix == "results/v1/123456789012/us-east-2/test/test-owner/" && aws_s3_bucket_lifecycle_configuration.results.rule[0].expiration[0].days == 30 && aws_s3_bucket_lifecycle_configuration.results.rule[0].abort_incomplete_multipart_upload[0].days_after_initiation == 1
    error_message = "Retain every scoped result for 30 days and abort incomplete multipart uploads after one day; do not expire runner artifacts."
  }
  assert {
    condition = toset([for s in jsondecode(aws_s3_bucket_policy.results.policy).Statement : s.Sid]) == toset(["DenyInsecureTransport", "RequireImmutableResultCreation", "RequireImmutableLaunchCreation"]) && alltrue([for s in jsondecode(aws_s3_bucket_policy.results.policy).Statement :
      s.Effect == "Deny" && s.Principal == "*" && s.Action == "s3:PutObject" && s.Resource == "${local.results_arn}/${local.results_prefix}*" && s.Condition.StringNotEquals["s3:if-none-match"] == "*" if s.Sid == "RequireImmutableResultCreation"
      ]) && alltrue([for s in jsondecode(aws_s3_bucket_policy.results.policy).Statement :
      s.Effect == "Deny" && s.Principal == "*" && s.Action == "s3:*" && s.Condition.Bool["aws:SecureTransport"] == "false" && s.Resource == [local.results_arn, "${local.results_arn}/*"] if s.Sid == "DenyInsecureTransport"
    ])
    error_message = "The bucket must deny insecure transport and missing/non-star If-None-Match writes across the complete result prefix."
  }
  assert {
    condition     = aws_s3_object.runner.key == "artifacts/runner/${filesha256(var.runner_path)}/linux-amd64" && aws_s3_object.runner.metadata.sha256 == filesha256(var.runner_path) && aws_s3_object.runner.source_hash == filesha256(var.runner_path) && !startswith(aws_s3_object.runner.key, local.results_prefix)
    error_message = "The real runner artifact must be content-addressed, hash-pinned and outside result expiration."
  }
  assert {
    condition     = jsondecode(local.worker_config).execution == output.deployment_manifest.execution && jsondecode(local.worker_config).results == output.deployment_manifest.results && jsondecode(local.worker_config).account == var.account_id && jsondecode(local.worker_config).region == var.region && jsondecode(local.worker_config).deployment == var.deployment && jsondecode(local.worker_config).owner == var.owner
    error_message = "Root worker configuration must have the same scope and pins as the CLI export."
  }
  assert {
    condition     = aws_launch_template.agent.user_data == base64encode(local.bootstrap) && output.deployment_manifest.bootstrap_sha256 == sha256(local.bootstrap)
    error_message = "The launch template and manifest must bind the fully rendered worker configuration and bootstrap."
  }
  assert {
    condition     = strcontains(local.bootstrap, "snap install aws-cli --classic") && strcontains(local.bootstrap, "sha256sum --check --status") && strcontains(local.bootstrap, "chmod 0600 /etc/devbox/execution.json") && strcontains(local.bootstrap, "ge 3.3.2746.0") && !strcontains(local.bootstrap, "@@DEVBOX_") && length(local.bootstrap) <= 16384
    error_message = "Bootstrap must install its downloader, verify the real artifact, protect configuration, require ENV_VAR-capable SSM, and fit EC2 user data."
  }
  assert {
    condition     = length(jsondecode(local.execution).mainSteps) == 1 && one(jsondecode(local.execution).mainSteps).name == "execute" && one(jsondecode(local.execution).mainSteps).inputs.runCommand == ["exec /usr/local/libexec/devbox-runner --config /etc/devbox/execution.json"] && one(jsondecode(local.execution).mainSteps).inputs.timeoutSeconds == "{{ stepTimeoutSeconds }}"
    error_message = "Only a fixed runner command may reach shell source; step timeout is a separate numeric property."
  }
  assert {
    condition     = toset(keys(jsondecode(local.execution).parameters)) == toset(["requestId", "payload", "stepTimeoutSeconds"]) && jsondecode(local.execution).parameters.requestId.interpolationType == "ENV_VAR" && jsondecode(local.execution).parameters.payload.interpolationType == "ENV_VAR" && jsondecode(local.execution).parameters.payload.maxChars == 32768 && !can(jsondecode(local.execution).parameters.stepTimeoutSeconds.interpolationType)
    error_message = "Only literal request/payload values use ENV_VAR; decimal timeout must retain normal document substitution."
  }
  assert {
    condition     = alltrue([for n in [181, 199, 200, 999, 1000, 9999, 10000, 79999, 80000, 85999, 86000, 86499, 86500, 86579, 86580] : can(regex(jsondecode(local.execution).parameters.stepTimeoutSeconds.allowedPattern, tostring(n)))]) && alltrue([for n in [0, 5, 180, 86581, 99999, 172800] : !can(regex(jsondecode(local.execution).parameters.stepTimeoutSeconds.allowedPattern, tostring(n)))]) && !can(regex(jsondecode(local.execution).parameters.stepTimeoutSeconds.allowedPattern, "181;id"))
    error_message = "SSM hard timeout accepts only decimal 181 through 86580, including the reserve."
  }
  assert {
    condition     = length([for s in jsondecode(local.instance_policy).Statement : s if s.Sid == "PublishExecutionResults" && s.Action == ["s3:PutObject"] && toset(s.Resource) == toset(local.worker_write_arns)]) == 1 && length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "PrepareExecution" && s.Action == ["s3:PutObject"] && toset(s.Resource) == toset(local.operator_write_arns)]) == 1 && length([for s in concat(jsondecode(local.operator_policy).Statement, jsondecode(local.instance_policy).Statement) : s if contains(s.Action, "s3:DeleteObject") || contains(s.Action, "ssm:CancelCommand")]) == 0
    error_message = "Worker and operator must own separate immutable object writes, with no result deletion or cancellation grants."
  }
  assert {
    condition = alltrue([for policy in [local.instance_policy, local.operator_policy] :
      length([for s in jsondecode(policy).Statement : s if s.Action == ["s3:GetObject"] && toset(s.Resource) == toset(local.result_object_arns)]) == 1 &&
      length([for s in jsondecode(policy).Statement : s if s.Sid == "ReadPinnedRunner" && s.Action == ["s3:GetObject"] && s.Resource == ["${local.results_arn}/${local.runner_key}"]]) == 1 &&
      length([for s in jsondecode(policy).Statement : s if s.Sid == "ListOwnedResults" && s.Action == ["s3:ListBucket"] && s.Resource == [local.results_arn] && toset(flatten([s.Condition.StringLike["s3:prefix"]])) == (policy == local.operator_policy ? toset(["${local.results_prefix}*", "${local.launch_ledger_prefix}*"]) : toset(["${local.results_prefix}*"]))]) == 1
    ])
    error_message = "Both roles need exact result reads, the pinned artifact only, and prefix-restricted result listings."
  }
  assert {
    condition     = length([for s in jsondecode(local.operator_policy).Statement : s if contains(s.Action, "ssm:SendCommand")]) == 3 && length([for s in jsondecode(local.operator_policy).Statement : s if s.Sid == "ExecutionDocument" && s.Resource == [aws_ssm_document.execution.arn] && s.Action == ["ssm:SendCommand", "ssm:GetDocument"]]) == 1
    error_message = "Exec preserves scoped instance authorization and only adds the exact execution document alongside readiness."
  }
}

run "retention_minimum" {
  command = plan
  variables { result_retention_days = 2 }
  assert {
    condition     = aws_s3_bucket_lifecycle_configuration.results.rule[0].expiration[0].days == 2
    error_message = "The configurable minimum is two full days."
  }
}
run "retention_too_short" {
  command = plan
  variables { result_retention_days = 1 }
  expect_failures = [var.result_retention_days]
}
run "retention_too_long" {
  command = plan
  variables { result_retention_days = 366 }
  expect_failures = [var.result_retention_days]
}
run "retention_fractional" {
  command = plan
  variables { result_retention_days = 2.5 }
  expect_failures = [var.result_retention_days]
}

override_resource {
  target = aws_iam_role.cleanup
  values = { arn = "arn:aws:iam::123456789012:role/devbox-test-test-owner-cleanup" }
}
override_resource {
  target = aws_iam_role.cleanup_scheduler
  values = { arn = "arn:aws:iam::123456789012:role/devbox-test-test-owner-schedule" }
}
override_resource {
  target = aws_lambda_function.cleanup
  values = { arn = "arn:aws:lambda:us-east-2:123456789012:function:devbox-test-test-owner-cleanup" }
}
