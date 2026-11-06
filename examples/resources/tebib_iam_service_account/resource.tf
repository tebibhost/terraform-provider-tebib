resource "tebib_iam_service_account" "ci" {
  name        = "github-actions"
  description = "Deploys from CI"
}
