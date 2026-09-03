data "logfire_pagerduty_service" "on_call" {
  account_subdomain    = "acme"
  region               = "us"
  pagerduty_service_id = "PABC123"
}

resource "logfire_channel" "pagerduty_app" {
  name = "pagerduty-app-on-call"

  config {
    type       = "pagerduty-integration"
    install_id = data.logfire_pagerduty_service.on_call.install_id
    service_id = data.logfire_pagerduty_service.on_call.service_id
  }
}
