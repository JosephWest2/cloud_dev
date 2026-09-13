locals {
  runner_sha256 = filesha256(var.runner_path)
  runner_key    = "artifacts/runner/${local.runner_sha256}/linux-amd64"
  execution = jsonencode({
    schemaVersion = "2.2"
    description   = "Literal devbox command protocol v1; pinned local runner publishes durable results"
    parameters = {
      requestId = {
        type           = "String", interpolationType = "ENV_VAR"
        allowedPattern = "^dc1-[0-9a-f]{32}$", minChars = 36, maxChars = 36
      }
      payload = {
        type           = "String", interpolationType = "ENV_VAR"
        allowedPattern = "^[A-Za-z0-9+/]+={0,2}$", minChars = 4, maxChars = 32768
      }
      # This is a numeric plugin property, never shell source. ENV_VAR would
      # prevent the agent's timeout parser from receiving a decimal number.
      stepTimeoutSeconds = {
        type           = "String", minChars = 3, maxChars = 5
        allowedPattern = "^(18[1-9]|19[0-9]|[2-9][0-9]{2}|[1-9][0-9]{3}|[1-7][0-9]{4}|8[0-5][0-9]{3}|86[0-4][0-9]{2}|865[0-7][0-9]|86580)$"
      }
    }
    mainSteps = [{
      action       = "aws:runShellScript", name = "execute"
      precondition = { StringEquals = ["platformType", "Linux"] }
      inputs = {
        timeoutSeconds = "{{ stepTimeoutSeconds }}"
        runCommand     = ["exec /usr/local/libexec/devbox-runner --config /etc/devbox/execution.json"]
      }
    }]
  })
  execution_manifest = {
    name                  = aws_ssm_document.execution.name
    version               = tostring(aws_ssm_document.execution.latest_version)
    content_sha256        = sha256(local.execution)
    step                  = "execute"
    runner_sha256         = local.runner_sha256
    minimum_agent_version = "3.3.2746.0"
  }
  worker_config = jsonencode({
    schema_version = 1
    account        = var.account_id
    region         = var.region
    deployment     = var.deployment
    owner          = var.owner
    execution      = local.execution_manifest
    results        = local.results_manifest
  })
}

resource "aws_ssm_document" "execution" {
  name            = "${local.name}-execute"
  document_type   = "Command"
  document_format = "JSON"
  content         = local.execution
  tags            = local.tags
}

resource "aws_s3_object" "runner" {
  bucket                 = aws_s3_bucket.results.id
  key                    = local.runner_key
  source                 = var.runner_path
  source_hash            = local.runner_sha256
  content_type           = "application/octet-stream"
  server_side_encryption = "AES256"
  metadata               = { sha256 = local.runner_sha256 }
  tags                   = local.tags
  depends_on = [
    aws_s3_bucket_public_access_block.results,
    aws_s3_bucket_ownership_controls.results,
    aws_s3_bucket_server_side_encryption_configuration.results,
    aws_s3_bucket_versioning.results,
    aws_s3_bucket_policy.results,
  ]
  lifecycle { create_before_destroy = true }
}
