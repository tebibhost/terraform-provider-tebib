resource "tebib_load_balancer" "web" {
  name = "web"
}

resource "tebib_load_balancer_target_group" "web" {
  load_balancer_id = tebib_load_balancer.web.id
  name             = "web"
  port             = 80
  health_check = {
    protocol = "http"
    path     = "/healthz"
  }
}

resource "tebib_load_balancer_listener" "http" {
  load_balancer_id = tebib_load_balancer.web.id
  port             = 80
  target_group_id  = tebib_load_balancer_target_group.web.id
}

resource "tebib_load_balancer_target" "web" {
  for_each         = { for vm in tebib_vm.web : vm.name => vm.id }
  load_balancer_id = tebib_load_balancer.web.id
  target_group_id  = tebib_load_balancer_target_group.web.id
  vm_id            = each.value
}

output "address" {
  value = tebib_load_balancer.web.public_address
}
