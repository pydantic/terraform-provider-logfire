terraform {
  required_providers {
    logfire = {
      source = "registry.terraform.io/pydantic/logfire"
    }
  }
}

provider "logfire" {
  base_url     = "https://logfire-us.pydantic.dev"
  api_key        = "pylf_v1_us_XjBb97dwvhKm9tv3Rsv4p3Y7YtWTkSr4klMdpk82nXHj"
  organization = "bruno-espino"
  project      = "logfire-provider"
}

resource "logfire_alert" "high_error_rate" {
  name         = "high-error-rate"
  description  = "Alert when error rate exceeds threshold"
  query        = "select message from records where message ilike '%unexpected%'"
  time_window  = "24h"
  frequency    = "24h"
  watermark    = "30s"
  channel_ids  = []
  notify_when  = "matches_changed"
  active       = true
}

