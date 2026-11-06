resource "tebib_public_ip" "nat" {
  name = "nat"
}

resource "tebib_nat_gateway" "main" {
  name         = "main"
  subnet_id    = tebib_subnet.web.id
  public_ip_id = tebib_public_ip.nat.id
}
