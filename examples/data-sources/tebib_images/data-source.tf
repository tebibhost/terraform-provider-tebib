data "tebib_images" "ubuntu" {
  os_family = "ubuntu"
}

output "ubuntu_images" {
  value = data.tebib_images.ubuntu.names
}
