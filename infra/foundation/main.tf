locals {
  name = "devbox-${var.deployment}-${var.owner}"
  tags = { ManagedBy = "devbox", Deployment = var.deployment, Owner = var.owner }
  ec2  = "arn:aws:ec2:${var.region}:${var.account_id}"
}

data "aws_ami" "ubuntu" {
  owners = ["099720109477"]
  filter {
    name   = "image-id"
    values = [var.ami_id]
  }
  lifecycle {
    postcondition {
      condition = (self.architecture == "x86_64" && self.virtualization_type == "hvm" && self.root_device_type == "ebs" &&
      can(regex("^ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-[0-9.]+$", self.name)))
      error_message = "AMI must be Canonical Ubuntu 24.04 amd64 HVM EBS server with recorded release provenance."
    }
  }
}

resource "aws_vpc" "devbox" {
  cidr_block           = "10.77.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = local.tags
}
resource "aws_subnet" "devbox" {
  vpc_id                  = aws_vpc.devbox.id
  cidr_block              = "10.77.1.0/24"
  availability_zone       = "${var.region}a"
  map_public_ip_on_launch = false # Only the template requests a worker public IP.
  tags                    = local.tags
}
resource "aws_internet_gateway" "devbox" {
  vpc_id = aws_vpc.devbox.id
  tags   = local.tags
}
resource "aws_route_table" "devbox" {
  vpc_id = aws_vpc.devbox.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.devbox.id
  }
  tags = local.tags
}
resource "aws_route_table_association" "devbox" {
  subnet_id      = aws_subnet.devbox.id
  route_table_id = aws_route_table.devbox.id
}
resource "aws_security_group" "devbox" {
  name        = local.name
  description = "No inbound connections; SSH travels over SSM"
  vpc_id      = aws_vpc.devbox.id
  ingress     = []
  egress {
    description = "Ubuntu package repositories"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  egress {
    description = "SSM and development HTTPS traffic"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = local.tags
}

resource "aws_launch_template" "agent" {
  name                   = local.name
  description            = "Ubuntu 24.04 x86_64; dedicated SSH key and durable command runner"
  image_id               = data.aws_ami.ubuntu.id
  update_default_version = true
  user_data              = base64encode(local.bootstrap)
  iam_instance_profile {
    arn = aws_iam_instance_profile.devbox.arn
  }
  network_interfaces {
    device_index                = 0
    subnet_id                   = aws_subnet.devbox.id
    security_groups             = [aws_security_group.devbox.id]
    associate_public_ip_address = true
    delete_on_termination       = true
  }
  block_device_mappings {
    device_name = data.aws_ami.ubuntu.root_device_name
    ebs {
      volume_type           = "gp3"
      volume_size           = 100
      encrypted             = true
      delete_on_termination = true
    }
  }
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "disabled"
  }
  # Market/type and all seven creation tags are supplied by the future launch CLI.
  tags       = local.tags
  depends_on = [aws_s3_object.runner, aws_s3_bucket_lifecycle_configuration.results]
  lifecycle {
    precondition {
      condition     = alltrue([for b in data.aws_ami.ubuntu.block_device_mappings : b.ebs.volume_size <= 100 if b.device_name == data.aws_ami.ubuntu.root_device_name])
      error_message = "AMI root snapshot exceeds the template's 100 GiB root disk."
    }
  }
}

locals {
  bootstrap = replace(replace(replace(replace(replace(replace(
    file("${path.module}/bootstrap.sh"),
    "@@DEVBOX_PUBLIC_KEY@@", var.ssh_public_key),
    "@@DEVBOX_RUNNER_BUCKET@@", local.results_bucket),
    "@@DEVBOX_REGION@@", var.region),
    "@@DEVBOX_ACCOUNT@@", var.account_id),
    "@@DEVBOX_RUNNER_SHA256@@", local.runner_sha256),
  "@@DEVBOX_WORKER_CONFIG@@", local.worker_config)
  readiness = jsonencode({
    schemaVersion = "2.2"
    description   = "Read-only devbox bootstrap status and public host key; output schema 1; no parameters"
    mainSteps = [{
      action = "aws:runShellScript", name = "bootstrapStatus"
      inputs = {
        timeoutSeconds = "10"
        runCommand = [<<-PROBE
          status=pending
          host_key=''
          if test -f /var/lib/devbox/bootstrap-failed; then
            status=failed
          elif test -f /var/lib/devbox/bootstrap-complete; then
            status=complete
            read -r key_type key_data key_comment < /etc/ssh/ssh_host_ed25519_key.pub || exit 1
            test "$key_type" = ssh-ed25519 || exit 1
            case "$key_data" in *[!A-Za-z0-9+/=]*|'') exit 1;; esac
            host_key="$key_type $key_data"
          fi
          printf '{"schema_version":1,"bootstrap":"%s","host_key":"%s"}\n' "$status" "$host_key"
        PROBE
        ]
      }
    }]
  })
}
resource "aws_ssm_document" "readiness" {
  name            = "${local.name}-readiness"
  document_type   = "Command"
  document_format = "JSON"
  content         = local.readiness
  tags            = local.tags
}
