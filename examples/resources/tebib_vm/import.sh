# The API never returns user_data: after an import, add it to
# lifecycle { ignore_changes = [user_data] } or the VM is replaced.
terraform import tebib_vm.web vm-abc123
