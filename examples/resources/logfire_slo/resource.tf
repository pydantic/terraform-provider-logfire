resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_channel" "alerts" {
  name = "alerts-webhook"

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://hooks.example.com/alerts"
  }
}

# An SLO has three burn-rate alerts: `fast` and `medium` (severity `page`)
# and `slow` (severity `ticket`). The provider writes each configured tier's
# channel assignments to its alert and reads them back, so a change made on
# the Logfire alerts page shows as drift in the next plan. A tier that is not
# configured keeps its channels.

# 1. The same channels on every alert, through a local.
locals {
  everyone = [{ channel_id = logfire_channel.alerts.id }]
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

  alerts = {
    fast   = { channel_assignments = local.everyone }
    medium = { channel_assignments = local.everyone }
    slow   = { channel_assignments = local.everyone }
  }
}

# 2. Different channels per tier.
resource "logfire_channel" "pagerduty" {
  name = "pagerduty"

  config {
    type        = "pagerduty"
    routing_key = var.pagerduty_routing_key
  }
}

resource "logfire_channel" "incidents" {
  name = "incidents-webhook"

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://hooks.example.com/incidents"
  }
}

resource "logfire_channel" "reliability" {
  name = "reliability-webhook"

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://hooks.example.com/reliability"
  }
}

resource "logfire_slo" "checkout_errors" {
  project_id     = logfire_project.example.id
  scope_value    = "checkout"
  name           = "checkout-errors"
  total_query    = "parent_span_id IS NULL"
  bad_query      = "otel_status_code = 'ERROR'"
  target_percent = "99.9"
  rolling_window = "30d"

  alerts = {
    fast = {
      channel_assignments = [
        { channel_id = logfire_channel.pagerduty.id },
        { channel_id = logfire_channel.incidents.id },
      ]
    }
    medium = { channel_assignments = [{ channel_id = logfire_channel.incidents.id }] }
    slow   = { channel_assignments = [{ channel_id = logfire_channel.reliability.id }] }
  }
}

# 3. Delivery schedules, shared with a normal alert. PagerDuty gets every
# fast and medium burn. The incidents channel gets them only during office
# hours, and the reliability channel gets slow burns during office hours. A
# normal alert reuses the same configuration, because both resources use the
# same assignment type.
resource "logfire_schedule" "office_hours" {
  label    = "Office hours"
  timezone = "Europe/London"
  windows = [
    { days = [1, 2, 3, 4, 5], start_time = "09:00", end_time = "18:00" },
  ]
}

locals {
  oncall = [
    { channel_id = logfire_channel.pagerduty.id },
    { channel_id = logfire_channel.incidents.id, schedule_id = logfire_schedule.office_hours.id },
  ]
}

resource "logfire_slo" "checkout" {
  project_id     = logfire_project.example.id
  scope_value    = "checkout"
  name           = "checkout-availability"
  total_query    = "parent_span_id IS NULL"
  bad_query      = "otel_status_code = 'ERROR'"
  target_percent = "99.9"
  rolling_window = "30d"

  alerts = {
    fast   = { channel_assignments = local.oncall }
    medium = { channel_assignments = local.oncall }
    slow = {
      channel_assignments = [
        { channel_id = logfire_channel.reliability.id, schedule_id = logfire_schedule.office_hours.id },
      ]
    }
  }
}

resource "logfire_alert" "payment_errors" {
  project_id          = logfire_project.example.id
  name                = "payment-errors"
  query               = "select trace_id from records where span_name = 'payment failed'"
  time_window         = "5m"
  frequency           = "1m"
  notify_when         = "has_matches"
  channel_assignments = local.oncall
}

# A histogram-threshold metric SLI: "95% of queue-latency observations under
# 60s". Uses `threshold` + `comparison` instead of `bad_query`, and requires
# `source = "metrics"`. It configures no tier, so the provider leaves the
# channels of its alerts as they are.
resource "logfire_slo" "queue_latency" {
  project_id         = logfire_project.example.id
  scope_value        = "ingest"
  name               = "queue-latency-under-60s"
  source             = "metrics"
  metric_aggregation = "histogram_threshold"
  total_query        = "metric_name = 'queue.latency'"
  threshold          = "60000"
  comparison         = "less_than"
  target_percent     = "95"
  rolling_window     = "30d"
}

variable "pagerduty_routing_key" {
  type      = string
  sensitive = true
}
