resource "tebib_iam_access_key" "ci" {
  service_account_id = tebib_iam_service_account.ci.id
  description        = "GitHub Actions"
  allowed_cidrs      = ["192.0.2.0/24"]
}

# The secret is in the state (sensitive). Hand it to the consumer, e.g. a CI secret.
output "ci_access_key_id" {
  value = tebib_iam_access_key.ci.id
}

output "ci_secret_access_key" {
  value     = tebib_iam_access_key.ci.secret
  sensitive = true
}
