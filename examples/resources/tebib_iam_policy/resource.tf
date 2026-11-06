resource "tebib_iam_policy" "vm_operator" {
  name        = "vm-operator"
  description = "Start, stop and reboot VMs"
  document = jsonencode({
    Version = "2026-10-08"
    Statement = [{
      Effect   = "Allow"
      Action   = ["compute:StartVM", "compute:StopVM", "compute:RebootVM", "compute:Describe*"]
      Resource = "*"
    }]
  })
}
