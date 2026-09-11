mock_provider "aws" {
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
variables {
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
    condition     = aws_launch_template.agent.image_id == var.ami_id && aws_launch_template.agent.user_data == filebase64("${path.module}/bootstrap.sh")
    error_message = "Pin the selected AMI and reviewed bootstrap."
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
      if contains(["TaggedInstances", "TaggedEncryptedVolumes"], s.Sid)
    ])
    error_message = "Instances and volumes need scoped and dynamic creation tags."
  }
  assert {
    condition = alltrue([for s in jsondecode(aws_iam_role_policy.operator.policy).Statement :
      s.Condition.StringEquals["ec2:ResourceTag/Owner"] == var.owner && s.Condition.StringEquals["ec2:ResourceTag/Deployment"] == var.deployment
      if s.Sid == "TerminateOwnedInstances"
    ])
    error_message = "Termination must require matching owner and deployment."
  }
  assert {
    condition     = length(local.operator_policy) <= 10240 && length(local.instance_policy) <= 10240
    error_message = "Inline policies exceed IAM role quotas."
  }
}
