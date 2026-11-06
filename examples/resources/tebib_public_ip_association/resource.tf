resource "tebib_public_ip_association" "web" {
  public_ip_id = tebib_public_ip.web.id
  vm_id        = tebib_vm.web.id

  # A VM's public IP reaches it through the VPC's NAT gateway.
  depends_on = [tebib_nat_gateway.main]
}
