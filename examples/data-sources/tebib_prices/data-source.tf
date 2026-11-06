data "tebib_prices" "now" {}

output "b1_small_per_month" {
  value = data.tebib_prices.now.monthly_usd_by_meter["vm.b1.small"]
}
