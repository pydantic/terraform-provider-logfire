# Cap the Gateway spend of the whole organization. A window you omit has no limit.
resource "logfire_gateway_spending_limits" "this" {
  daily_usd   = 500
  monthly_usd = 10000
}
