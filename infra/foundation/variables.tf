variable "account_id" {
  type = string
  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "Select a 12-digit test account ID."
  }
}
variable "region" {
  type    = string
  default = "us-east-2"
  validation {
    condition     = var.region == "us-east-2"
    error_message = "This foundation currently supports Ohio (us-east-2) only."
  }
}
variable "deployment" {
  type = string
  validation {
    condition     = can(regex("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,22}$", var.deployment))
    error_message = "Use 1-23 letters, digits, underscores or hyphens, starting with a letter or digit."
  }
}
variable "owner" {
  type = string
  validation {
    condition     = can(regex("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,22}$", var.owner))
    error_message = "Use a stable owner of 1-23 letters, digits, underscores or hyphens."
  }
}
variable "operator_principal_arn" {
  type        = string
  description = "Existing IAM user or role allowed to assume the operator role; never an STS session or account root."
  validation {
    condition     = can(regex("^arn:aws:iam::${var.account_id}:(user|role)/[A-Za-z0-9+=,.@_/-]+$", var.operator_principal_arn))
    error_message = "Select an existing IAM user/role ARN in the test account."
  }
}
variable "ami_id" {
  type        = string
  description = "Exact regional Canonical Ubuntu 24.04 amd64 server AMI, resolved and reviewed during setup. No latest lookup at apply."
  validation {
    condition     = can(regex("^ami-[0-9a-f]{17}$", var.ami_id))
    error_message = "Resolve and paste an exact regional AMI ID."
  }
}

variable "ssh_public_key" {
  type        = string
  description = "Dedicated user-managed Ed25519 public key, exactly type and base64; never a private key."
  validation {
    condition     = can(regex("^ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI[A-P][A-Za-z0-9+/]{42}$", var.ssh_public_key))
    error_message = "Supply one Ed25519 public key without its comment or newline (the first two fields of the .pub file)."
  }
}

variable "runner_path" {
  type        = string
  default     = "../../bin/devbox-runner-linux-amd64"
  description = "Linux/amd64 runner built with make runner; relative paths are resolved from infra/foundation. Build before planning and keep the file unchanged through apply."
  validation {
    condition     = fileexists(var.runner_path)
    error_message = "Build the real Linux/amd64 runner with make runner before planning, or set runner_path to that binary."
  }
}

variable "result_retention_days" {
  type        = number
  default     = 30
  description = "Result retention from request creation, 2-365 whole days. Never shorten while previous retention promises remain unexpired."
  validation {
    condition     = var.result_retention_days >= 2 && var.result_retention_days <= 365 && floor(var.result_retention_days) == var.result_retention_days
    error_message = "Set result_retention_days to an integer from 2 through 365."
  }
}

variable "availability_zones" {
  type        = list(string)
  default     = ["us-east-2a", "us-east-2b", "us-east-2c"]
  description = "One subnet per selected Ohio AZ. Original us-east-2a subnet remains in state even when omitted; removing an additional AZ requires reviewing its subnet/worker impact."
  validation {
    condition     = length(var.availability_zones) >= 1 && length(var.availability_zones) <= 3 && length(distinct(var.availability_zones)) == length(var.availability_zones) && alltrue([for zone in var.availability_zones : contains(["us-east-2a", "us-east-2b", "us-east-2c"], zone)])
    error_message = "Select 1–3 distinct standard Ohio AZs: us-east-2a, us-east-2b, us-east-2c."
  }
}

variable "instance_types" {
  type        = list(string)
  default     = ["c7i.2xlarge", "c7a.2xlarge", "c6i.2xlarge", "c6a.2xlarge"]
  description = "Explicit compatible instance-type pool. Actual capabilities and AZ offerings are read at plan time and verified again by doctor."
  validation {
    condition     = length(var.instance_types) >= 1 && length(distinct(var.instance_types)) == length(var.instance_types) && alltrue([for typ in var.instance_types : can(regex("^[a-z0-9-]+\\.[a-z0-9-]+$", typ))])
    error_message = "Provide a nonempty list of distinct EC2 instance-type names."
  }
}

variable "root_disk_gb" {
  type        = number
  default     = 100
  description = "Pinned template gp3 root size in GiB; must fit the selected AMI. Profile overrides still require encrypted disposable gp3 and the AMI minimum."
  validation {
    condition     = var.root_disk_gb >= 8 && var.root_disk_gb <= 16384 && floor(var.root_disk_gb) == var.root_disk_gb
    error_message = "Template root_disk_gb must be an integer from 8 through 16384."
  }
}
