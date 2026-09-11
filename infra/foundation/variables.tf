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
