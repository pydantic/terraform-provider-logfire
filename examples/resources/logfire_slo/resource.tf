resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_slo" "example" {
  project_id     = logfire_project.example.id
  scope_value    = "payments-api"
  name           = "payments-availability"
  description    = "Successful request ratio for the payments API"
  total_query    = "parent_span_id IS NULL"
  bad_query      = "otel_status_code = 'ERROR'"
  target_percent = "99.9"
  rolling_window = "30d"
  environments   = ["prod"]
}
