data "tebib_storage" "this" {}

output "s3_endpoint" {
  value = data.tebib_storage.this.endpoint
}
