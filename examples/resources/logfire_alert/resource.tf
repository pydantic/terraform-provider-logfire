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

resource "logfire_channel" "office" {
  name = "office-hours-webhook"

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/logfire-office-webhook"
  }
}

resource "logfire_schedule" "office_hours" {
  label    = "Office hours"
  timezone = "Europe/London"
  windows = [
    { days = [1, 2, 3, 4, 5], start_time = "09:00", end_time = "18:00" },
  ]
}

resource "logfire_alert" "example" {
  project_id   = logfire_project.example.id
  name         = "error-alert"
  description  = "Alert on exception spans"
  query        = <<-SQL
    select
      service_name,
      trace_id,
      otel_status_message as exception_message
    from records
    where level = 'error'
    order by start_timestamp desc
  SQL
  time_window  = "1h"
  frequency    = "15m"
  environments = ["production"]
  channel_assignments = [
    # Notify this channel at all times.
    { channel_id = logfire_channel.example.id },
    # Notify this channel only inside the schedule's windows.
    { channel_id = logfire_channel.office.id, schedule_id = logfire_schedule.office_hours.id },
  ]
  notify_when = "has_matches"
  active      = true
}

# Track each failing service as its own occurrence. A run that opens or closes
# occurrences sends one notification listing them. Needs an organization with
# alert instance policies enabled.
resource "logfire_alert" "per_service" {
  project_id  = logfire_project.example.id
  name        = "error-alert-per-service"
  query       = "select service_name from records where level = 'error' group by service_name"
  time_window = "1h"
  frequency   = "15m"
  channel_assignments = [
    { channel_id = logfire_channel.example.id },
  ]
  notify_when = "instances_changed"
  instance_policy = {
    identity_columns = ["service_name"]
    pending_runs     = 2
    missing_runs     = 3
    missing_behavior = "no_longer_matching"
  }
}
