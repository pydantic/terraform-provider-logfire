resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_channel" "example" {
  name = "alerts-webhook"

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/logfire-webhook"
  }
}

resource "logfire_alert" "example" {
  project_id  = logfire_project.example.id
  name        = "error-alert"
  description = "Alert on exception spans"
  query       = <<-SQL
    select
      service_name,
      trace_id,
      otel_status_message as exception_message
    from records
    where level = 'error'
    order by start_timestamp desc
  SQL
  time_window = "1h"
  frequency   = "15m"
  # Optional: only evaluate the query against these deployment environments.
  environments = ["production"]
  channel_ids  = [logfire_channel.example.id]
  notify_when  = "has_matches"
  active       = true
}
