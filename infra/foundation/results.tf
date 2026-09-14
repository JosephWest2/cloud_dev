locals {
  results_bucket       = "devbox-results-${var.account_id}-${var.region}-${substr(sha256("${var.deployment}/${var.owner}"), 0, 16)}"
  results_prefix       = "results/v1/${var.account_id}/${var.region}/${var.deployment}/${var.owner}/"
  results_arn          = "arn:aws:s3:::${local.results_bucket}"
  launch_ledger_prefix = "launches/v2/${var.account_id}/${var.region}/${var.deployment}/${var.owner}/"
  launch_ledger_arn    = "${local.results_arn}/${local.launch_ledger_prefix}*"
  results_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "DenyInsecureTransport", Effect = "Deny", Principal = "*", Action = "s3:*"
        Resource  = [local.results_arn, "${local.results_arn}/*"]
        Condition = { Bool = { "aws:SecureTransport" = "false" } }
      },
      {
        Sid      = "RequireImmutableResultCreation", Effect = "Deny", Principal = "*", Action = "s3:PutObject"
        Resource = "${local.results_arn}/${local.results_prefix}*"
        # StringNotEquals also denies a missing header. There is deliberately no
        # multipart exemption: the bounded runner uses single-object PUTs.
        Condition = { StringNotEquals = { "s3:if-none-match" = "*" } }
      },
      {
        Sid       = "RequireImmutableLaunchCreation", Effect = "Deny", Principal = "*", Action = "s3:PutObject"
        Resource  = local.launch_ledger_arn
        Condition = { StringNotEquals = { "s3:if-none-match" = "*" } }
      },
    ]
  })
  results_manifest = {
    schema_version        = 1
    bucket                = aws_s3_bucket.results.bucket
    expected_bucket_owner = var.account_id
    region                = var.region
    prefix                = local.results_prefix
    retention_days        = var.result_retention_days
    policy_sha256         = sha256(local.results_policy)
  }
  launch_ledger_manifest = {
    schema_version        = 1
    bucket                = aws_s3_bucket.results.bucket
    expected_bucket_owner = var.account_id
    region                = var.region
    prefix                = local.launch_ledger_prefix
    policy_sha256         = sha256(local.results_policy)
  }
  result_object_arns = [for name in ["request.json", "acknowledgement.json", "started.json", "outcome.json", "stdout", "stderr", "result.json"] :
    "${local.results_arn}/${local.results_prefix}dc1-*/${name}"
  ]
  worker_write_arns = [for name in ["started.json", "outcome.json", "stdout", "stderr", "result.json"] :
    "${local.results_arn}/${local.results_prefix}dc1-*/${name}"
  ]
  operator_write_arns = [for name in ["request.json", "acknowledgement.json"] :
    "${local.results_arn}/${local.results_prefix}dc1-*/${name}"
  ]
}

resource "aws_s3_bucket" "results" {
  bucket        = local.results_bucket
  force_destroy = false
  tags          = merge(local.tags, { Purpose = "command-results" })
}
resource "aws_s3_bucket_public_access_block" "results" {
  bucket                  = aws_s3_bucket.results.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_ownership_controls" "results" {
  bucket = aws_s3_bucket.results.id
  rule { object_ownership = "BucketOwnerEnforced" }
}
resource "aws_s3_bucket_versioning" "results" {
  bucket = aws_s3_bucket.results.id
  # AWS cannot return an already-versioned bucket to this state. Drift requires
  # a deliberate storage migration, not suspension with hidden old versions.
  versioning_configuration { status = "Disabled" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "results" {
  bucket = aws_s3_bucket.results.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_s3_bucket_policy" "results" {
  bucket = aws_s3_bucket.results.id
  policy = local.results_policy
}
resource "aws_s3_bucket_lifecycle_configuration" "results" {
  bucket = aws_s3_bucket.results.id
  rule {
    id     = "command-results-retention"
    status = "Enabled"
    # Launch records have no expiration: removing a dispatch claim can permit
    # duplicate workers from a separate client. Delete only during deliberate
    # deployment removal using the setup identity.
    filter { prefix = local.results_prefix }
    expiration { days = var.result_retention_days }
    abort_incomplete_multipart_upload { days_after_initiation = 1 }
  }
}
