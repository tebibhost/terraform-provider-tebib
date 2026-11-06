resource "tebib_iam_policy_attachment" "ci_vm_operator" {
  policy     = tebib_iam_policy.vm_operator.id
  principal  = tebib_iam_service_account.ci.trn
  project_id = "p-def456" # leave out for the whole organization
}
