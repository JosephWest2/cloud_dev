terraform {
  required_version = "= 1.12.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "= 6.64.0"
    }
  }
  # Start locally; copy backend.tf.example only after creating the bucket.
}
provider "aws" {
  region              = "us-east-2"
  allowed_account_ids = [var.account_id]
}
variable "account_id" {
  type = string
  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "Select the 12-digit test account ID."
  }
}
variable "bucket_name" {
  type = string
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.bucket_name))
    error_message = "Choose a unique lowercase S3 bucket name (letters, digits and hyphens)."
  }
}
resource "aws_s3_bucket" "state" {
  bucket        = var.bucket_name
  force_destroy = false
  tags          = { ManagedBy = "devbox", Purpose = "infrastructure-state" }
  lifecycle { prevent_destroy = true }
}
resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule { object_ownership = "BucketOwnerEnforced" }
}
resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration { status = "Enabled" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [{
    Sid       = "DenyInsecureTransport", Effect = "Deny", Principal = "*", Action = "s3:*"
    Resource  = [aws_s3_bucket.state.arn, "${aws_s3_bucket.state.arn}/*"]
    Condition = { Bool = { "aws:SecureTransport" = "false" } }
  }] })
}
output "bucket_name" { value = aws_s3_bucket.state.id }
