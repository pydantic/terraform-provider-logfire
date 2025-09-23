terraform {
  required_providers {
    logfire = {
      source = "registry.terraform.io/pydantic/logfire"
    }
  }
}

# Supports env vars:
# export LOGFIRE_BASE_URL="https://logfire-us.pydantic.dev"
# export LOGFIRE_API_KEY="pylf_v1_…"
provider "logfire" {
  base_url     = "https://logfire-us.pydantic.dev"
  api_key        = "pylf_v1_…"
}

variable organization {
    default = "bruno-espino"
}

resource "logfire_project" "staging" {
  organization = var.organization
  project_name = "staging"
  description = "staging project"
}

resource "logfire_project" "production" {
  organization = var.organization
  project_name = "production"
  description = "prod project"
}

resource "logfire_alert" "staging_high_error_rate" {
  organization = var.organization
  project      = logfire_project.staging.project_name
  name         = "high-error-rate"
  description  = "Alert when error rate exceeds threshold"
  query        = "select message from records where message ilike '%unexpected%'"
  time_window  = "24h"
  frequency    = "6h"
  watermark    = "30s"
  channel_ids  = []
  notify_when  = "matches_changed"
  active       = true
}

resource "logfire_alert" "production_high_error_rate" {
  organization = var.organization
  project      = logfire_project.production.project_name
  name         = "high-error-rate"
  description  = "Alert when error rate exceeds threshold"
  query        = "select message from records where message ilike '%unexpected%'"
  time_window  = "24h"
  frequency    = "6h"
  watermark    = "30s"
  channel_ids  = []
  notify_when  = "matches_changed"
  active       = true
}

# resource "logfire_channel" "my_channel" {
#   organization = var.organization
#   label = "my-channel"
#   config {
#     url = "http://localhost:8080"
#     type = "webhook"
#     format = "auto"
#   }
# }