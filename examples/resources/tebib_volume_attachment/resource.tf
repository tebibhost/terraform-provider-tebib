resource "tebib_volume_attachment" "data" {
  volume_id = tebib_volume.data.id
  vm_id     = tebib_vm.web.id
}
