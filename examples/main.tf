terraform {
  #required_version = ">= 1.5"

  required_providers {
    logfire = {
      source = "registry.terraform.io/pydantic/logfire"
      # version = "~> 0.1"
    }
  }
}

# Configure via env vars:
#   export LOGFIRE_BASE_URL="https://logfire-us.pydantic.dev"
#   export LOGFIRE_API_KEY="pylf_v1_..."
provider "logfire" {
  base_url = "https://logfire-us.pydantic.dev"
  # api_key  = "pylf_v1_..."
}

resource "logfire_project" "production" {
  name        = "production"
  description = "Production observability projects"
}

resource "logfire_channel" "alerts_webhook" {
  name   = "alerts-webhooks"
  active = true

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/logfire-webhook"
  }
}

resource "logfire_alert" "production_alert_execution_failures" {
  project_id  = logfire_project.production.id
  name        = "execution-errorss"
  description = "Fire when spans named 'Alert execution error occurred' are ingested"
  # Heredoc strings for multiline queries https://developer.hashicorp.com/terraform/language/expressions/strings#heredoc-strings
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
  time_window = "1h"
  frequency   = "15m"
  channel_ids = [logfire_channel.alerts_webhook.id]
  notify_when = "has_matches"
  active      = true
}

resource "logfire_dashboard" "production_overview" {
  project_id = logfire_project.production.id
  name       = "production-overview"
  slug       = "production-overview"
  # Export a dashboard JSON from Logfire; its metadata.name is replaced by the value above.
  definition = file("${path.module}/example.json")
}

resource "logfire_write_token" "production_ingest" {
  project_id = logfire_project.production.id
}

output "production_write_token" {
  description = "Write token for sending data to the production project"
  value       = logfire_write_token.production_ingest.token
  sensitive   = true
}
