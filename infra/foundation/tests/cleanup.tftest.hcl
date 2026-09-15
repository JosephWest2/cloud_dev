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
run "cleanup_boundary" {
  command = apply
  assert {
    condition = (alltrue([for keyword in ["schedule-arn", "scheduled-time", "execution-id", "attempt-number"] :
      strcontains(one(aws_scheduler_schedule.cleanup.target).input, "<aws.scheduler.${keyword}>")
      ]) && sha256(jsonencode(jsondecode(one(aws_scheduler_schedule.cleanup.target).input))) == local.cleanup_manifest.schedule.input_sha256
    )
    error_message = "Scheduler transport must contain literal substitution keywords while the manifest hashes canonical JSON."
  }
  assert {
    condition     = aws_lambda_function.cleanup.runtime == "provided.al2023" && one(aws_lambda_function.cleanup.architectures) == "x86_64" && aws_lambda_function.cleanup.handler == "bootstrap" && aws_lambda_function.cleanup.source_code_hash == filebase64sha256(local.cleanup_zip)
    error_message = "The executable ZIP architecture/runtime and update digest must agree."
  }
  assert {
    condition     = aws_lambda_function.cleanup.timeout == 180 && aws_lambda_function.cleanup.reserved_concurrent_executions == 1 && aws_lambda_function_event_invoke_config.cleanup.maximum_retry_attempts == 0 && aws_lambda_function_event_invoke_config.cleanup.maximum_event_age_in_seconds == 300
    error_message = "Lambda execution budgets must be bounded separately from Scheduler delivery."
  }
  assert {
    condition     = aws_scheduler_schedule.cleanup.state == "DISABLED" && aws_scheduler_schedule.cleanup.schedule_expression == "rate(5 minutes)" && one(aws_scheduler_schedule.cleanup.flexible_time_window).mode == "OFF" && one(one(aws_scheduler_schedule.cleanup.target).retry_policy).maximum_retry_attempts == 2 && one(one(aws_scheduler_schedule.cleanup.target).retry_policy).maximum_event_age_in_seconds == 300
    error_message = "Schedule must be disabled pending independent failure evidence, with bounded delivery and no flexible window."
  }
  assert {
    condition     = one(jsondecode(local.cleanup_scheduler_trust).Statement).Principal.Service == "scheduler.amazonaws.com" && one(jsondecode(local.cleanup_scheduler_trust).Statement).Condition.StringEquals == { "aws:SourceAccount" = var.account_id, "aws:SourceArn" = local.cleanup_group_arn } && jsondecode(local.cleanup_scheduler_policy).Statement[0].Resource == local.cleanup_arn && jsondecode(local.cleanup_scheduler_policy).Statement[0].Action == ["lambda:InvokeFunction"]
    error_message = "Scheduler invocation must trust the exact account/group and grant only the exact function."
  }
  assert {
    condition     = toset(flatten([for s in jsondecode(local.cleanup_policy).Statement : s.Action])) == toset(["ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:TerminateInstances", "logs:CreateLogStream", "logs:PutLogEvents", "sqs:SendMessage"]) && length([for s in jsondecode(local.cleanup_policy).Statement : s if s.Action == ["ec2:TerminateInstances"] && s.Resource == "${local.ec2}:instance/*" && s.Condition.StringEquals == merge(local.resource_scope, { "aws:RequestedRegion" = var.region })]) == 1
    error_message = "Cleanup must have exact regional/tag termination scope, without launch, retag, volume delete or S3 mutation permissions."
  }
  assert {
    condition     = length([for s in jsondecode(local.cleanup_policy).Statement : s if s.Resource == "*" && s.Action == ["ec2:DescribeInstances", "ec2:DescribeVolumes"] && s.Condition.StringEquals["aws:RequestedRegion"] == var.region]) == 1 && length([for s in jsondecode(local.cleanup_policy).Statement : s if s.Action == ["logs:CreateLogStream", "logs:PutLogEvents"] && s.Resource == "${local.cleanup_log_arn}:log-stream:*"]) == 1
    error_message = "Only required Describe APIs may use wildcard resources; log writes stay in the exact group."
  }
  assert {
    condition     = aws_cloudwatch_log_group.cleanup.retention_in_days == 30 && aws_cloudwatch_log_group.cleanup.skip_destroy && one(jsondecode(local.cleanup_trust).Statement).Principal.Service == "lambda.amazonaws.com"
    error_message = "Retain cleanup evidence and use supported Lambda service trust."
  }
}
run "cleanup_retention_invalid" {
  command = plan
  variables { cleanup_log_retention_days = 0 }
  expect_failures = [var.cleanup_log_retention_days]
}
run "cleanup_retention_fraction" {
  command = plan
  variables { cleanup_log_retention_days = 7.5 }
  expect_failures = [var.cleanup_log_retention_days]
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

override_resource {
  target = aws_iam_role.evidence
  values = { arn = "arn:aws:iam::123456789012:role/devbox-test-test-owner-evidence" }
}
override_resource {
  target = aws_sqs_queue.cleanup_failures
  values = { arn = "arn:aws:sqs:us-east-2:123456789012:devbox-test-test-owner-evidence" }
}
run "retained_failure_route" {
  command = apply
  assert {
    condition = (aws_sqs_queue.cleanup_failures.sqs_managed_sse_enabled && !aws_sqs_queue.cleanup_failures.fifo_queue && aws_sqs_queue.cleanup_failures.message_retention_seconds == 1209600 && aws_sqs_queue.cleanup_failures.visibility_timeout_seconds == 1800
      && one(one(aws_scheduler_schedule.cleanup.target).dead_letter_config).arn == local.evidence_queue_arn
    && one(one(aws_lambda_function_event_invoke_config.cleanup.destination_config).on_failure).destination == local.evidence_queue_arn)
    error_message = "Both pre-handler failures must reach the same encrypted 14-day transport with conservative visibility."
  }
  assert {
    condition = (aws_pipes_pipe.cleanup_failures.source == local.evidence_queue_arn && aws_pipes_pipe.cleanup_failures.target == local.evidence_log_arn && aws_pipes_pipe.cleanup_failures.desired_state == "RUNNING"
      && one(one(aws_pipes_pipe.cleanup_failures.source_parameters).sqs_queue_parameters).batch_size == 1
      && length(one(aws_pipes_pipe.cleanup_failures.source_parameters).filter_criteria) == 0
      && one(one(aws_pipes_pipe.cleanup_failures.target_parameters).cloudwatch_logs_parameters).log_stream_name == "failures"
    && aws_cloudwatch_log_group.cleanup_failures.skip_destroy && aws_cloudwatch_log_group.cleanup_failures.retention_in_days == 30)
    error_message = "A no-filter batch-1 running Pipe must target the retained failure log stream."
  }
  assert {
    condition = (one(jsondecode(local.evidence_trust).Statement).Condition.StringEquals == { "aws:SourceAccount" = var.account_id, "aws:SourceArn" = local.evidence_pipe_arn }
      && jsondecode(local.evidence_policy).Statement[0].Action == ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]
      && jsondecode(local.evidence_policy).Statement[0].Resource == local.evidence_queue_arn
      && jsondecode(local.evidence_policy).Statement[1].Action == ["logs:CreateLogStream", "logs:PutLogEvents"]
      && jsondecode(local.evidence_policy).Statement[1].Resource == "${local.evidence_log_arn}:log-stream:failures"
      && length([for s in jsondecode(local.cleanup_policy).Statement : s if s.Action == ["sqs:SendMessage"] && s.Resource == local.evidence_queue_arn]) == 1
    && jsondecode(local.cleanup_scheduler_policy).Statement[1].Action == ["sqs:SendMessage"] && jsondecode(local.cleanup_scheduler_policy).Statement[1].Resource == local.evidence_queue_arn)
    error_message = "Pipe trust and both producers must stay scoped to the exact route."
  }
  assert {
    condition = (one(jsondecode(local.health_trust).Statement).Principal.AWS == aws_iam_role.operator.arn
      && length([for s in jsondecode(local.operator_policy).Statement : s if s.Action == ["sts:AssumeRole"] && s.Resource == [local.health_role_arn]]) == 1
      && length(local.operator_policy) <= 10240 && length(local.health_policy) <= 10240
      && alltrue([for s in jsondecode(local.health_policy).Statement : s.Effect == "Allow"])
    && toset(flatten([for s in jsondecode(local.health_policy).Statement : s.Action])) == toset(["lambda:GetFunctionConfiguration", "lambda:GetFunctionConcurrency", "lambda:GetFunctionEventInvokeConfig", "scheduler:GetSchedule", "pipes:DescribePipe", "sqs:GetQueueAttributes", "logs:FilterLogEvents", "logs:DescribeLogStreams", "logs:DescribeMetricFilters", "iam:GetRole", "iam:ListRolePolicies", "iam:GetRolePolicy", "iam:ListAttachedRolePolicies", "cloudwatch:DescribeAlarms", "logs:DescribeLogGroups", "cloudwatch:GetMetricStatistics"]))
    error_message = "Health credentials must be operator-only and read-only, within aggregate IAM quotas."
  }
  assert {
    condition = (length(aws_cloudwatch_metric_alarm.cleanup) == 16
      && aws_cloudwatch_metric_alarm.cleanup["scheduler-TargetErrorCount"].dimensions == tomap({ ScheduleGroup = local.cleanup_name })
      && aws_cloudwatch_metric_alarm.cleanup["lambda-Errors"].dimensions == tomap({ FunctionName = local.cleanup_name })
      && aws_cloudwatch_metric_alarm.cleanup["pipe-ExecutionTimeout"].dimensions == tomap({ PipeName = local.evidence_name })
      && aws_cloudwatch_metric_alarm.cleanup["queue-ApproximateAgeOfOldestMessage"].dimensions == tomap({ QueueName = local.evidence_name })
      && aws_cloudwatch_metric_alarm.cleanup["no-success"].treat_missing_data == "breaching"
      && aws_cloudwatch_metric_alarm.cleanup["no-success"].period == 300 && aws_cloudwatch_metric_alarm.cleanup["no-success"].evaluation_periods == 3
      && aws_cloudwatch_metric_alarm.cleanup["no-success"].comparison_operator == "LessThanThreshold"
    && aws_cloudwatch_metric_alarm.cleanup["no-success"].threshold == 1)
    error_message = "Use supported metric dimensions and alarm on three missing five-minute successes."
  }
}
run "evidence_retention_configurable" {
  command = plan
  variables { cleanup_log_retention_days = 60 }
  assert {
    condition     = aws_cloudwatch_log_group.cleanup.retention_in_days == 60 && aws_cloudwatch_log_group.cleanup_failures.retention_in_days == 60 && local.evidence_manifest.logs.retention_days == 60
    error_message = "Export and retain the selected handler/failure retention together."
  }
}
