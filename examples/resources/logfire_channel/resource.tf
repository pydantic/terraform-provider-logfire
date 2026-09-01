variable "pagerduty_routing_key" {
  type      = string
  sensitive = true
}

resource "logfire_channel" "example" {
  name   = "alerts-webhook"
  active = true

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/logfire-webhook"
  }
}

resource "logfire_channel" "pagerduty" {
  name   = "pagerduty-on-call"
  active = true

  config {
    type        = "pagerduty"
    routing_key = var.pagerduty_routing_key
    region      = "us"
  }
}

resource "logfire_channel" "slack_app" {
  name   = "slack-app-alerts"
  active = true

  config {
    type       = "slack-integration"
    install_id = "018f9a6a-1234-7890-abcd-ef0123456789"
    channel_id = "C0123456789"
  }
}

resource "logfire_channel" "pagerduty_app" {
  name   = "pagerduty-app-on-call"
  active = true

  config {
    type       = "pagerduty-integration"
    install_id = "018f9a6a-1234-7890-abcd-ef0123456789"
    service_id = "018f9a6a-4321-7890-abcd-ef0123456789"
  }
}
