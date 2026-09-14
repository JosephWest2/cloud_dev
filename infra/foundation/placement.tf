data "aws_availability_zones" "selected" {
  state = "available"
}

data "aws_ec2_instance_type" "selected" {
  for_each      = toset(var.instance_types)
  instance_type = each.key
  lifecycle {
    postcondition {
      condition = (contains(self.supported_architectures, "x86_64") && contains(self.supported_virtualization_types, "hvm") &&
        contains(self.supported_root_device_types, "ebs") && self.ebs_encryption_support == "supported" &&
        contains(["supported", "required"], self.ena_support) && self.maximum_network_interfaces >= 1 && self.maximum_ipv4_addresses_per_interface >= 1 &&
      contains(self.supported_usages_classes, "spot") && contains(self.supported_usages_classes, "on-demand"))
      error_message = "Types must support x86_64 HVM, encrypted EBS, ENA/IPv4 and both Spot and On-Demand usage."
    }
  }
}

data "aws_ec2_instance_type_offerings" "selected" {
  for_each      = toset(var.availability_zones)
  location_type = "availability-zone"
  filter {
    name   = "location"
    values = [each.key]
  }
  filter {
    name   = "instance-type"
    values = var.instance_types
  }
}

locals {
  subnets_by_zone = merge({ "us-east-2a" = aws_subnet.devbox.id }, { for zone, subnet in aws_subnet.additional : zone => subnet.id })
  selected_subnets = [for zone in sort(var.availability_zones) : {
    id = local.subnets_by_zone[zone], availability_zone = zone
  }]
  subnet_ids = [for subnet in local.selected_subnets : subnet.id]
  compatible_pools = [for typ in var.instance_types : {
    instance_type = typ
    architecture  = "x86_64"
    subnet_ids = [for zone in sort(var.availability_zones) : local.subnets_by_zone[zone]
      if contains(data.aws_ec2_instance_type_offerings.selected[zone].instance_types, typ)
    ]
  }]
}
