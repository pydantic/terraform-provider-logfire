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
  base_url = "https://logfire-us.pydantic.dev"
  #  api_key        = "pylf_v1_…"
}

variable "organization" {
  default = "bruno-espino"
}

resource "logfire_project" "production" {
  organization = var.organization
  project_name = "production"
  description  = "prod project"
}

resource "logfire_alert" "production_alert_execution_failures" {
  organization = var.organization
  project      = logfire_project.production.project_name
  name         = "production-alert-execution-errors"
  description  = "Production: fires on spans named 'Alert execution error occurred' to capture alert execution failures"
  /// Heredoc strings for multiline queries https://developer.hashicorp.com/terraform/language/expressions/strings#heredoc-strings
  query       = <<-SQL
    select
      service_name,
      trace_id,
      otel_status_message as exception_message
    from records
    where deployment_environment = 'prod'
      and span_name = 'Alert execution error occurred'
    order by start_timestamp desc
  SQL
  time_window = "24h"
  frequency   = "6h"
  watermark   = "30s"
  channel_ids = []
  notify_when = "has_matches"
  active      = true
}

resource "logfire_channel" "my_channel" {
  organization = var.organization
  project      = logfire_project.production.project_name
  label        = "production-slack"
  config {
    url    = "http://localhost:8012"
    type   = "webhook"
    format = "auto"
  }
}