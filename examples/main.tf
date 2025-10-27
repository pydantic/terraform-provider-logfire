terraform {
  required_providers {
    logfire = {
      source = "registry.terraform.io/pydantic/logfire"
    }
  }
}

# You can configure these values directly or via environment variables:
#   export LOGFIRE_BASE_URL="https://logfire-us.pydantic.dev"
#   export LOGFIRE_API_KEY="pylf_v1_..."
provider "logfire" {
  base_url = "https://logfire-us.pydantic.dev"
  #  api_key  = "pylf_v1_..."
}

variable "organization" {
  default     = "terraform-provider-logfire"
  description = "Logfire organization"
}

resource "logfire_project" "production" {
  organization = var.organization
  name         = "production"
  description  = "prod project"
}

resource "logfire_channel" "alerts_webhook" {
  organization = var.organization
  project      = logfire_project.production.name
  name         = "alerts-webhook"
  config {
    url    = "https://hooks.slack.com/services/xxxx/xxx/xxx"
    type   = "webhook"
    format = "auto"
  }
}

resource "logfire_alert" "production_alert_execution_failures" {
  organization = var.organization
  project      = logfire_project.production.name
  name         = "execution-errors"
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
  channel_ids = [logfire_channel.alerts_webhook.id]
  notify_when = "has_matches"
  active      = true
}

resource "logfire_dashboard" "test_dashboard" {
  organization = var.organization
  project      = logfire_project.production.name
  name         = "my dashboard"
  slug         = "my-dashboard"

  # Export the dashboard JSON from the UI, save it beside this example, and load it directly.
  # The metadata.name and metadata.project inside the JSON will be replaced with the value from the `name` and `project` attributes above.
  definition = file("${path.module}/example.json")
}