resource "tebib_security_group_rule" "ssh" {
  security_group_id = tebib_security_group.web.id
  protocol          = "tcp"
  port_from         = 22
  remote_cidr       = "203.0.113.0/24"
  description       = "SSH from the office"
}

resource "tebib_security_group_rule" "https_v6" {
  security_group_id = tebib_security_group.web.id
  protocol          = "tcp"
  port_from         = 443
  remote_cidr       = "::/0"
}

# Members of the group can reach each other on any port.
resource "tebib_security_group_rule" "peers" {
  security_group_id = tebib_security_group.web.id
  protocol          = "all"
  remote_group_id   = tebib_security_group.web.id
  ip_version        = "ipv4"
}
