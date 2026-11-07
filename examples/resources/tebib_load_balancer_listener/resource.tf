resource "tebib_load_balancer_listener" "api" {
  load_balancer_id = tebib_load_balancer.web.id
  port             = 8443
  target_group_id  = tebib_load_balancer_target_group.api.id
}
