# Cap the Gateway spend of one project. A window you omit has no limit.
resource "logfire_gateway_project_spending_limits" "example" {
  project_id  = logfire_project.example.id
  weekly_usd  = 200
  monthly_usd = 1000
}
