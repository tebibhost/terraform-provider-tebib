# A web server in its own VPC, reachable over SSH and HTTP(S) on a public IP.
#
#   export TEBIB_ACCESS_KEY_ID=TBK... TEBIB_SECRET_ACCESS_KEY=... TEBIB_PROJECT=p-...
#   tofu init && tofu apply      # or: terraform init && terraform apply
#   ssh ubuntu@$(tofu output -raw public_ip)

terraform {
  required_providers {
    tebib = {
      source  = "tebibhost/tebib"
      version = "~> 0.1"
    }
  }
}

# Settings come from the environment (TEBIB_*) or your `tebib configure` profile.
provider "tebib" {}

variable "ssh_public_key" {
  description = "Your SSH public key. Default: ~/.ssh/id_ed25519.pub."
  type        = string
  default     = null
}

variable "admin_cidr" {
  description = "Where SSH is allowed from. Narrow it to your own address."
  type        = string
  default     = "0.0.0.0/0"
}

data "tebib_images" "ubuntu" {
  os_family = "ubuntu"
}

# -- network ------------------------------------------------------------------------------

resource "tebib_vpc" "web" {
  name = "web"
  cidr = "10.10.0.0/16"
}

resource "tebib_subnet" "web" {
  name   = "web"
  vpc_id = tebib_vpc.web.id
  cidr   = "10.10.1.0/24"
}

# Public IPs reach VMs through their VPC's NAT gateway, so the VPC needs one.
resource "tebib_public_ip" "nat" {
  name = "web-nat"
}

resource "tebib_nat_gateway" "web" {
  name         = "web"
  subnet_id    = tebib_subnet.web.id
  public_ip_id = tebib_public_ip.nat.id
}

resource "tebib_security_group" "web" {
  name        = "web"
  vpc_id      = tebib_vpc.web.id
  description = "SSH from admins, HTTP and HTTPS from anywhere"
}

resource "tebib_security_group_rule" "ssh" {
  security_group_id = tebib_security_group.web.id
  protocol          = "tcp"
  port_from         = 22
  remote_cidr       = var.admin_cidr
}

resource "tebib_security_group_rule" "http" {
  security_group_id = tebib_security_group.web.id
  protocol          = "tcp"
  port_from         = 80
  remote_cidr       = "0.0.0.0/0"
}

resource "tebib_security_group_rule" "https" {
  security_group_id = tebib_security_group.web.id
  protocol          = "tcp"
  port_from         = 443
  remote_cidr       = "0.0.0.0/0"
}

# -- server -------------------------------------------------------------------------------

resource "tebib_ssh_key" "me" {
  name       = "me"
  public_key = var.ssh_public_key != null ? var.ssh_public_key : file(pathexpand("~/.ssh/id_ed25519.pub"))
}

resource "tebib_vm" "web" {
  name               = "web-1"
  image              = data.tebib_images.ubuntu.names[0]
  instance_type      = "b1.small"
  subnet_id          = tebib_subnet.web.id
  security_group_ids = [tebib_security_group.web.id]
  ssh_key_ids        = [tebib_ssh_key.me.id]
  root_disk_gib      = 20

  user_data = <<-EOT
    #cloud-config
    packages: [nginx]
  EOT

  tags = {
    role = "web"
  }
}

resource "tebib_volume" "data" {
  name     = "web-1-data"
  size_gib = 50
}

resource "tebib_volume_attachment" "data" {
  volume_id = tebib_volume.data.id
  vm_id     = tebib_vm.web.id
}

resource "tebib_public_ip" "web" {
  name = "web-1"
}

resource "tebib_public_ip_association" "web" {
  public_ip_id = tebib_public_ip.web.id
  vm_id        = tebib_vm.web.id

  depends_on = [tebib_nat_gateway.web]
}

output "public_ip" {
  value = tebib_public_ip.web.address
}

output "private_ip" {
  value = tebib_vm.web.ipv4_address
}

output "ipv6" {
  value = tebib_vm.web.ipv6_address
}
