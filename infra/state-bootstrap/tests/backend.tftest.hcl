mock_provider "aws" {}
variables {
  account_id  = "123456789012"
  bucket_name = "devbox-test-fixture-only"
}
run "encrypted_versioned_private_state_plan" {
  command = plan
  assert {
    condition     = one(aws_s3_bucket_versioning.state.versioning_configuration).status == "Enabled"
    error_message = "State recovery requires versioning."
  }
  assert {
    condition     = one(one(aws_s3_bucket_server_side_encryption_configuration.state.rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256"
    error_message = "State must be encrypted."
  }
  assert {
    condition     = aws_s3_bucket_public_access_block.state.block_public_acls && aws_s3_bucket_public_access_block.state.block_public_policy && aws_s3_bucket_public_access_block.state.ignore_public_acls && aws_s3_bucket_public_access_block.state.restrict_public_buckets && !aws_s3_bucket.state.force_destroy
    error_message = "State must be private and protected against forced deletion."
  }
}
