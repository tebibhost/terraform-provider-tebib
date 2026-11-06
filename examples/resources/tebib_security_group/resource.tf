resource "tebib_security_group" "web" {
  name        = "web"
  vpc_id      = tebib_vpc.main.id
  description = "Web servers"
}
