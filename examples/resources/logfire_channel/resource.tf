resource "logfire_channel" "example" {
  name   = "alerts-webhook"
  active = true

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/logfire-webhook"
  }
}
