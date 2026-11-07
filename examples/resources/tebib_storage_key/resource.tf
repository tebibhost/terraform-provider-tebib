resource "tebib_iam_service_account" "app" {
  name = "app"
}

resource "tebib_storage_key" "app" {
  name               = "app"
  service_account_id = tebib_iam_service_account.app.id
}

data "tebib_storage" "this" {}

# For an S3 client (rclone, the AWS SDKs...): path-style URLs.
output "s3" {
  value = {
    endpoint          = data.tebib_storage.this.endpoint
    region            = data.tebib_storage.this.region
    access_key_id     = tebib_storage_key.app.access_key_id
    secret_access_key = tebib_storage_key.app.secret_access_key
  }
  sensitive = true
}
