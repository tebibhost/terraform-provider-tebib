data "tebib_instance_types" "four_cpus" {
  min_vcpus = 4
}

output "smallest_with_four_cpus" {
  value = data.tebib_instance_types.four_cpus.names[0]
}
