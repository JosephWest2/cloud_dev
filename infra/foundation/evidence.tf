# Failure evidence is independent of cleanup-handler initialization. The queue
# is transport; the Pipe's CloudWatch Logs TARGET is the retained record.
locals {
  evidence_name      = "${local.name}-evidence"
  evidence_queue_arn = "arn:aws:sqs:${var.region}:${var.account_id}:${local.evidence_name}"
  evidence_queue_url = "https://sqs.${var.region}.amazonaws.com/${var.account_id}/${local.evidence_name}"
  evidence_pipe_arn  = "arn:aws:pipes:${var.region}:${var.account_id}:pipe/${local.evidence_name}"
  evidence_log_name  = "/devbox/${local.name}/failures"
  evidence_log_arn   = "arn:aws:logs:${var.region}:${var.account_id}:log-group:${local.evidence_log_name}"
  health_role_arn    = "arn:aws:iam::${var.account_id}:role/${local.name}-health"
  # Do not jsonencode this template: Terraform escapes angle brackets. Pipes
  # substitutes each unquoted JSON value, including implicitly decoded SQS body.
  evidence_input = trimspace(<<-JSON
    {"schema_version":1,"kind":"invocation_failure","messageId":<$.messageId>,"body":<$.body>,"messageAttributes":<$.messageAttributes>,"SentTimestamp":<$.attributes.SentTimestamp>,"ingested_at":<aws.pipes.event.ingestion-time>,"pipe_arn":<aws.pipes.pipe-arn>,"source_arn":<aws.pipes.source-arn>}
  JSON
  )
  evidence_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect    = "Allow", Principal = { Service = "pipes.amazonaws.com" }, Action = "sts:AssumeRole"
    Condition = { StringEquals = { "aws:SourceAccount" = var.account_id, "aws:SourceArn" = local.evidence_pipe_arn } }
  }] })
  evidence_policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"], Resource = local.evidence_queue_arn },
    { Effect = "Allow", Action = ["logs:CreateLogStream", "logs:PutLogEvents"], Resource = "${local.evidence_log_arn}:log-stream:failures" }
  ] })
  health_trust = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect = "Allow", Principal = { AWS = aws_iam_role.operator.arn }, Action = "sts:AssumeRole"
  }] })
  health_policy = jsonencode({ Version = "2012-10-17", Statement = [
    {
      Effect   = "Allow", Action = ["lambda:GetFunctionConfiguration", "lambda:GetFunctionConcurrency", "lambda:GetFunctionEventInvokeConfig", "scheduler:GetSchedule", "pipes:DescribePipe", "sqs:GetQueueAttributes", "logs:FilterLogEvents", "logs:DescribeLogStreams", "logs:DescribeMetricFilters"]
      Resource = [local.cleanup_arn, "arn:aws:scheduler:${var.region}:${var.account_id}:schedule/${local.cleanup_name}/${local.cleanup_name}", local.evidence_pipe_arn, local.evidence_queue_arn, "${local.cleanup_log_arn}:*", "${local.evidence_log_arn}:*"]
    },
    {
      Effect   = "Allow", Action = ["iam:GetRole", "iam:ListRolePolicies", "iam:GetRolePolicy", "iam:ListAttachedRolePolicies"]
      Resource = [aws_iam_role.cleanup.arn, aws_iam_role.cleanup_scheduler.arn, aws_iam_role.evidence.arn, local.health_role_arn]
    },
    { Effect = "Allow", Action = ["cloudwatch:DescribeAlarms"], Resource = [for key, v in local.evidence_alarms : "arn:aws:cloudwatch:${var.region}:${var.account_id}:alarm:${local.cleanup_name}-${key}"] },
    {
      # These read APIs do not support resource-level permissions.
      Effect    = "Allow", Action = ["logs:DescribeLogGroups", "cloudwatch:GetMetricStatistics"], Resource = "*"
      Condition = { StringEquals = { "aws:RequestedRegion" = var.region } }
    }
  ] })
  evidence_success_pattern = "{ $.kind = \"invocation_end\" && $.ok IS TRUE && $.complete IS TRUE && $.partial IS FALSE && $.deadline IS FALSE && $.result.scan_complete IS TRUE && $.result.exit_code = 0 && $.result.dry_run IS FALSE && $.correlation.schedule_arn = \"arn:aws:scheduler:${var.region}:${var.account_id}:schedule/${local.cleanup_name}/${local.cleanup_name}\" && $.scope.account = \"${var.account_id}\" && $.scope.region = \"${var.region}\" && $.scope.deployment = \"${var.deployment}\" && $.scope.owner = \"${var.owner}\" }"
  evidence_alarms = merge(
    { for metric in ["TargetErrorCount", "InvocationDroppedCount", "InvocationsFailedToBeSentToDeadLetterCount"] : "scheduler-${metric}" => {
      namespace = "AWS/Scheduler", metric_name = metric, dimensions = { ScheduleGroup = local.cleanup_name }, statistic = "Sum", threshold = 0, comparison_operator = "GreaterThanThreshold", period = 60, evaluation_periods = 1, treat_missing_data = "notBreaching"
    } },
    { for metric in ["Errors", "Throttles", "AsyncEventsDropped", "DestinationDeliveryFailures"] : "lambda-${metric}" => {
      namespace = "AWS/Lambda", metric_name = metric, dimensions = { FunctionName = local.cleanup_name }, statistic = "Sum", threshold = 0, comparison_operator = "GreaterThanThreshold", period = 60, evaluation_periods = 1, treat_missing_data = "notBreaching"
    } },
    { for metric in ["ExecutionFailed", "ExecutionTimeout", "ExecutionPartiallyFailed", "TargetStageFailed"] : "pipe-${metric}" => {
      namespace = "AWS/Pipes", metric_name = metric, dimensions = { PipeName = local.evidence_name }, statistic = "Sum", threshold = 0, comparison_operator = "GreaterThanThreshold", period = 60, evaluation_periods = 1, treat_missing_data = "notBreaching"
    } },
    { for metric in ["ApproximateNumberOfMessagesVisible", "ApproximateNumberOfMessagesNotVisible", "ApproximateAgeOfOldestMessage"] : "queue-${metric}" => {
      namespace = "AWS/SQS", metric_name = metric, dimensions = { QueueName = local.evidence_name }, statistic = "Maximum", threshold = metric == "ApproximateAgeOfOldestMessage" ? 300 : 0, comparison_operator = "GreaterThanThreshold", period = 60, evaluation_periods = 1, treat_missing_data = "notBreaching"
    } },
    { scheduler-InvocationAttemptCount = {
      namespace = "AWS/Scheduler", metric_name = "InvocationAttemptCount", dimensions = { ScheduleGroup = local.cleanup_name }, statistic = "Sum", threshold = 1, comparison_operator = "LessThanThreshold", period = 300, evaluation_periods = 3, treat_missing_data = "breaching"
      }, no-success = {
      namespace = "Devbox/Cleanup/${local.cleanup_name}", metric_name = "SuccessfulCompletion", dimensions = {}, statistic = "Sum", threshold = 1, comparison_operator = "LessThanThreshold", period = 300, evaluation_periods = 3, treat_missing_data = "breaching"
    } }
  )
  evidence_manifest = {
    schema_version  = 1
    queue           = { name = local.evidence_name, arn = local.evidence_queue_arn, url = local.evidence_queue_url, retention_seconds = 1209600, visibility_seconds = 1800, sse_sqs = true }
    pipe            = { name = local.evidence_name, arn = local.evidence_pipe_arn, desired_state = "RUNNING", batch_size = 1, input_template = local.evidence_input }
    logs            = { name = local.evidence_log_name, arn = local.evidence_log_arn, retention_days = var.cleanup_log_retention_days }
    stream          = "failures"
    pipe_role       = { arn = aws_iam_role.evidence.arn, trust_sha256 = sha256(local.evidence_trust), policy_name = aws_iam_role_policy.evidence.name, policy_sha256 = sha256(local.evidence_policy) }
    health_role     = { arn = local.health_role_arn, trust_sha256 = sha256(local.health_trust), policy_name = aws_iam_role_policy.cleanup_health.name, policy_sha256 = sha256(local.health_policy) }
    success_filter  = { name = "successful-completion", pattern = local.evidence_success_pattern, namespace = local.evidence_alarms.no-success.namespace, metric_name = "SuccessfulCompletion" }
    alarms          = { for key, alarm in local.evidence_alarms : key => merge(alarm, { name = "${local.cleanup_name}-${key}", arn = "arn:aws:cloudwatch:${var.region}:${var.account_id}:alarm:${local.cleanup_name}-${key}" }) }
    delivery_metric = { namespace = "AWS/Scheduler", metric_name = "InvocationAttemptCount", dimensions = { ScheduleGroup = local.cleanup_name } }
  }
}
resource "aws_sqs_queue" "cleanup_failures" {
  name                       = local.evidence_name
  fifo_queue                 = false
  sqs_managed_sse_enabled    = true
  message_retention_seconds  = 1209600
  visibility_timeout_seconds = 1800
  tags                       = local.tags
}
resource "aws_cloudwatch_log_group" "cleanup_failures" {
  name              = local.evidence_log_name
  retention_in_days = var.cleanup_log_retention_days
  skip_destroy      = true
  tags              = local.tags
}
resource "aws_iam_role" "evidence" {
  name               = local.evidence_name
  assume_role_policy = local.evidence_trust
  tags               = local.tags
}
resource "aws_iam_role_policy" "evidence" {
  name   = "devbox-evidence"
  role   = aws_iam_role.evidence.id
  policy = local.evidence_policy
}
resource "aws_pipes_pipe" "cleanup_failures" {
  name          = local.evidence_name
  role_arn      = aws_iam_role.evidence.arn
  source        = aws_sqs_queue.cleanup_failures.arn
  target        = local.evidence_log_arn
  desired_state = "RUNNING"
  source_parameters {
    sqs_queue_parameters { batch_size = 1 }
  }
  target_parameters {
    input_template = local.evidence_input
    cloudwatch_logs_parameters { log_stream_name = "failures" }
  }
  tags       = local.tags
  depends_on = [aws_iam_role_policy.evidence, aws_cloudwatch_log_group.cleanup_failures]
}
resource "aws_iam_role" "cleanup_health" {
  name               = "${local.name}-health"
  assume_role_policy = local.health_trust
  tags               = local.tags
}
resource "aws_iam_role_policy" "cleanup_health" {
  name   = "devbox-health"
  role   = aws_iam_role.cleanup_health.id
  policy = local.health_policy
  lifecycle {
    precondition {
      condition     = length(local.health_policy) <= 10240
      error_message = "Cleanup health inline policy exceeds IAM's aggregate quota."
    }
  }
}
resource "aws_cloudwatch_log_metric_filter" "cleanup_success" {
  name           = "successful-completion"
  log_group_name = aws_cloudwatch_log_group.cleanup.name
  pattern        = local.evidence_success_pattern
  metric_transformation {
    name      = "SuccessfulCompletion"
    namespace = local.evidence_alarms.no-success.namespace
    value     = "1"
    unit      = "Count"
  }
}
resource "aws_cloudwatch_metric_alarm" "cleanup" {
  for_each            = local.evidence_alarms
  alarm_name          = "${local.cleanup_name}-${each.key}"
  alarm_description   = "Cleanup health: ${each.key}. Follow docs/runbooks/cleanup.md; delivery acceptance alone is not handler success."
  namespace           = each.value.namespace
  metric_name         = each.value.metric_name
  dimensions          = each.value.dimensions
  statistic           = each.value.statistic
  threshold           = each.value.threshold
  comparison_operator = each.value.comparison_operator
  period              = each.value.period
  evaluation_periods  = each.value.evaluation_periods
  datapoints_to_alarm = each.value.evaluation_periods
  treat_missing_data  = each.value.treat_missing_data
  tags                = local.tags
}
