resource "tebib_iam_role" "deploy" {
  name = "deploy"
  trust_policy = jsonencode({
    Version = "2026-10-08"
    Statement = [{
      Effect    = "Allow"
      Principal = { Tebib = tebib_iam_service_account.ci.trn }
      Action    = "iam:AssumeRole"
    }]
  })
  max_session_seconds = 3600
}
