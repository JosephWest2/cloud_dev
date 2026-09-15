variable "cleanup_schedule_enabled" {
  type        = bool
  default     = false
  description = "Enable only after issue #47 failure evidence and the reviewed acceptance migration are installed."
}
variable "cleanup_log_retention_days" {
  type    = number
  default = 30
  validation {
    condition     = contains([7, 14, 30, 60, 90, 120, 150, 180, 365], var.cleanup_log_retention_days)
    error_message = "Cleanup evidence retention must be one of 7,14,30,60,90,120,150,180,365 days."
  }
}
locals {
  cleanup_name      = "${local.name}-cleanup"
  cleanup_arn       = "arn:aws:lambda:${var.region}:${var.account_id}:function:${local.cleanup_name}"
  cleanup_group_arn = "arn:aws:scheduler:${var.region}:${var.account_id}:schedule-group/${local.cleanup_name}"
  cleanup_log_name  = "/aws/lambda/${local.cleanup_name}"
  cleanup_log_arn   = "arn:aws:logs:${var.region}:${var.account_id}:log-group:${local.cleanup_log_name}"
  cleanup_zip       = "${path.module}/../../bin/devbox-cleanup-linux-amd64.zip"
  cleanup_environment = {
    DEVBOX_ACCOUNT    = var.account_id, DEVBOX_REGION = var.region,
    DEVBOX_DEPLOYMENT = var.deployment, DEVBOX_OWNER = var.owner,
    DEVBOX_LOG_GROUP  = local.cleanup_log_name
  }
  cleanup_input_canonical = jsonencode({
    schema_version = 1, scheduled_time = "<aws.scheduler.scheduled-time>",
    schedule_arn   = "<aws.scheduler.schedule-arn>", execution_id = "<aws.scheduler.execution-id>",
    attempt_number = "<aws.scheduler.attempt-number>"
  })
  # Scheduler substitutes literal keywords in the transport string. jsonencode
  # escapes angle brackets; retain that canonical form only for digest checks.
  cleanup_input = replace(replace(local.cleanup_input_canonical, "\\u003c", "<"), "\\u003e", ">")
  cleanup_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect = "Allow", Principal = { Service = "lambda.amazonaws.com" }, Action = "sts:AssumeRole"
  }] })
  cleanup_scheduler_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect    = "Allow", Principal = { Service = "scheduler.amazonaws.com" }, Action = "sts:AssumeRole"
    Condition = { StringEquals = { "aws:SourceAccount" = var.account_id, "aws:SourceArn" = local.cleanup_group_arn } }
  }] })
  cleanup_policy = jsonencode({ Version = "2012-10-17", Statement = [
    {
      # EC2 Describe APIs do not support resource-level authorization.
      Effect    = "Allow", Action = ["ec2:DescribeInstances", "ec2:DescribeVolumes"], Resource = "*"
      Condition = { StringEquals = { "aws:RequestedRegion" = var.region } }
    },
    {
      Effect    = "Allow", Action = ["ec2:TerminateInstances"], Resource = "${local.ec2}:instance/*"
      Condition = { StringEquals = merge(local.resource_scope, { "aws:RequestedRegion" = var.region }) }
    },
    { Effect = "Allow", Action = ["logs:CreateLogStream", "logs:PutLogEvents"], Resource = "${local.cleanup_log_arn}:log-stream:*" },
    { Effect = "Allow", Action = ["sqs:SendMessage"], Resource = local.evidence_queue_arn }
  ] })
  cleanup_scheduler_policy = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect = "Allow", Action = ["lambda:InvokeFunction"], Resource = local.cleanup_arn
  }, { Effect = "Allow", Action = ["sqs:SendMessage"], Resource = local.evidence_queue_arn }] })
  cleanup_manifest = {
    schema_version = 1
    function = {
      arn                   = local.cleanup_arn, runtime = "provided.al2023", architecture = "x86_64"
      code_sha256           = filesha256(local.cleanup_zip), environment_sha256 = sha256(jsonencode(local.cleanup_environment))
      timeout_seconds       = 180, service_timeout_seconds = 165, reserved_concurrency = 1, memory_mb = 256
      async_max_age_seconds = 300, async_retry_attempts = 0
    }
    schedule = {
      arn             = "arn:aws:scheduler:${var.region}:${var.account_id}:schedule/${local.cleanup_name}/${local.cleanup_name}"
      name            = local.cleanup_name, group_name = local.cleanup_name, group_arn = local.cleanup_group_arn
      state           = var.cleanup_schedule_enabled ? "ENABLED" : "DISABLED", expression = "rate(5 minutes)", flexible_window = "OFF"
      max_age_seconds = 300, retry_attempts = 2, input_sha256 = sha256(local.cleanup_input_canonical)
    }
    logs = { name = local.cleanup_log_name, arn = local.cleanup_log_arn, retention_days = var.cleanup_log_retention_days }
    execution_role = {
      arn         = aws_iam_role.cleanup.arn, trust_sha256 = sha256(local.cleanup_trust)
      policy_name = aws_iam_role_policy.cleanup.name, policy_sha256 = sha256(local.cleanup_policy)
    }
    scheduler_role = {
      arn         = aws_iam_role.cleanup_scheduler.arn, trust_sha256 = sha256(local.cleanup_scheduler_trust)
      policy_name = aws_iam_role_policy.cleanup_scheduler.name, policy_sha256 = sha256(local.cleanup_scheduler_policy)
    }
    evidence = local.evidence_manifest
  }
}
resource "aws_cloudwatch_log_group" "cleanup" {
  name              = local.cleanup_log_name
  retention_in_days = var.cleanup_log_retention_days
  skip_destroy      = true
  tags              = local.tags
}
resource "aws_iam_role" "cleanup" {
  name               = local.cleanup_name
  assume_role_policy = local.cleanup_trust
  tags               = local.tags
}
resource "aws_iam_role_policy" "cleanup" {
  name   = "devbox-cleanup"
  role   = aws_iam_role.cleanup.id
  policy = local.cleanup_policy
}
resource "aws_iam_role" "cleanup_scheduler" {
  name               = "${local.name}-schedule"
  assume_role_policy = local.cleanup_scheduler_trust
  tags               = local.tags
}
resource "aws_iam_role_policy" "cleanup_scheduler" {
  name   = "devbox-schedule"
  role   = aws_iam_role.cleanup_scheduler.id
  policy = local.cleanup_scheduler_policy
}
resource "aws_lambda_function" "cleanup" {
  function_name                  = local.cleanup_name
  role                           = aws_iam_role.cleanup.arn
  runtime                        = "provided.al2023"
  handler                        = "bootstrap"
  architectures                  = ["x86_64"]
  filename                       = local.cleanup_zip
  source_code_hash               = filebase64sha256(local.cleanup_zip)
  timeout                        = 180
  memory_size                    = 256
  reserved_concurrent_executions = 1
  environment { variables = local.cleanup_environment }
  tags       = local.tags
  depends_on = [aws_iam_role_policy.cleanup, aws_cloudwatch_log_group.cleanup]
}
resource "aws_lambda_function_event_invoke_config" "cleanup" {
  function_name                = aws_lambda_function.cleanup.function_name
  maximum_event_age_in_seconds = 300
  maximum_retry_attempts       = 0
  destination_config {
    on_failure { destination = aws_sqs_queue.cleanup_failures.arn }
  }
}
resource "aws_scheduler_schedule_group" "cleanup" {
  name = local.cleanup_name
  tags = local.tags
}
resource "aws_scheduler_schedule" "cleanup" {
  name                = local.cleanup_name
  group_name          = aws_scheduler_schedule_group.cleanup.name
  state               = var.cleanup_schedule_enabled ? "ENABLED" : "DISABLED"
  schedule_expression = "rate(5 minutes)"
  flexible_time_window { mode = "OFF" }
  target {
    arn      = aws_lambda_function.cleanup.arn
    role_arn = aws_iam_role.cleanup_scheduler.arn
    input    = local.cleanup_input
    dead_letter_config { arn = aws_sqs_queue.cleanup_failures.arn }
    retry_policy {
      maximum_event_age_in_seconds = 300
      maximum_retry_attempts       = 2
    }
  }
  depends_on = [aws_iam_role_policy.cleanup_scheduler, aws_lambda_function_event_invoke_config.cleanup]
}
