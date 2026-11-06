resource "tebib_subnet" "web" {
  name   = "web"
  vpc_id = tebib_vpc.main.id
  cidr   = "10.1.1.0/24"
}
