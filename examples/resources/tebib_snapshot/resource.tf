resource "tebib_snapshot" "before_upgrade" {
  name  = "before-upgrade"
  vm_id = tebib_vm.web.id
}
