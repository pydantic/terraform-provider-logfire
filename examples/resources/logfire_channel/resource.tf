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
