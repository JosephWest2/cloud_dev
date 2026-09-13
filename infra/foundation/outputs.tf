output "deployment_manifest" {
  description = "Non-secret CLI contract; export with tofu output -json deployment_manifest."
  value = {
    schema_version       = 4
    account              = var.account_id
    region               = var.region
    deployment           = var.deployment
    owner                = var.owner
    vpc_id               = aws_vpc.devbox.id
    subnet_ids           = [aws_subnet.devbox.id]
    security_group_id    = aws_security_group.devbox.id
    route_table_id       = aws_route_table.devbox.id
    internet_gateway_id  = aws_internet_gateway.devbox.id
    instance_profile_arn = aws_iam_instance_profile.devbox.arn
    development_user     = "devbox"
    ssh_public_key       = var.ssh_public_key
    bootstrap_sha256     = sha256(local.bootstrap)
    readiness = {
      name           = aws_ssm_document.readiness.name
      version        = tostring(aws_ssm_document.readiness.latest_version)
      content_sha256 = sha256(local.readiness)
    }
    execution = local.execution_manifest
    results   = local.results_manifest
    roles = {
      instance = {
        arn           = aws_iam_role.instance.arn
        trust_sha256  = sha256(local.instance_trust)
        policy_name   = aws_iam_role_policy.instance.name
        policy_sha256 = sha256(local.instance_policy)
      }
      operator = {
        arn           = aws_iam_role.operator.arn
        trust_sha256  = sha256(local.operator_trust)
        policy_name   = aws_iam_role_policy.operator.name
        policy_sha256 = sha256(local.operator_policy)
      }
    }
    images = {
      agent = {
        ami_id                  = data.aws_ami.ubuntu.id
        architecture            = "x86_64"
        ubuntu_release          = "24.04"
        owner_account           = "099720109477"
        name                    = data.aws_ami.ubuntu.name
        root_device_name        = data.aws_ami.ubuntu.root_device_name
        launch_template_id      = aws_launch_template.agent.id
        launch_template_version = tostring(aws_launch_template.agent.latest_version)
      }
    }
  }
  depends_on = [aws_iam_role_policy.operator, aws_iam_role_policy.instance, aws_route_table_association.devbox, aws_s3_object.runner, aws_s3_bucket_lifecycle_configuration.results]
}
