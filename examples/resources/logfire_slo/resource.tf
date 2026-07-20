resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_channel" "oncall" {
  name = "oncall-webhook"

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://hooks.example.com/oncall"
  }
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

  # Seed the generated burn-rate alerts' delivery channels (create-time only;
  # delivery is alert-owned afterwards).
  page_channel_ids   = [logfire_channel.oncall.id]
  ticket_channel_ids = [logfire_channel.oncall.id]
}
