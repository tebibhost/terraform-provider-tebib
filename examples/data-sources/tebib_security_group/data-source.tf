data "tebib_security_group" "default" {
  default = true
  vpc_id  = data.tebib_vpc.default.id
}
