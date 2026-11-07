resource "tebib_load_balancer_target" "api" {
  load_balancer_id = tebib_load_balancer.web.id
  target_group_id  = tebib_load_balancer_target_group.api.id
  vm_id            = tebib_vm.api.id
  port             = 9000
}
