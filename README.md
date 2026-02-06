# Terraform Provider for Logfire

Manage [Pydantic Logfire](https://pydantic.dev/logfire) projects, alerting, dashboards, and API tokens with Terraform. The provider is published at `registry.terraform.io/pydantic/logfire`.

## Requirements
- Terraform CLI 1.5 or newer
- A Logfire account and API key (via config or `LOGFIRE_BASE_URL`/`LOGFIRE_API_KEY`)

## Quick Start
```hcl
terraform {
  required_providers {
    logfire = {
      source  = "registry.terraform.io/pydantic/logfire"
      version = ">= 0.1.0, < 0.2.0"
    }
  }
}

provider "logfire" {
  # Or set LOGFIRE_BASE_URL / LOGFIRE_API_KEY env vars.
  base_url = "https://logfire-us.pydantic.dev"
  api_key  = "pylf_v1_..."
}

resource "logfire_project" "prod" {
  name         = "production"
  description  = "Prod observability project"
}

resource "logfire_channel" "alerts" {
  name   = "alerts-webhook"
  # Optional, defaults to true.
  active = true
  config {
    # Also supports "opsgenie" (with `auth_key`). Email channels are not available yet.
    type   = "webhook"
    format = "auto"
    url    = "https://hooks.slack.com/services/xxx/yyy/zzz"
  }
}

resource "logfire_alert" "execution_errors" {
  project_id   = logfire_project.prod.id
  name         = "execution-errors"
  query        = <<-SQL
    select
      service_name,
      trace_id,
      otel_status_message as exception_message
    from records
    where deployment_environment = 'prod'
      and span_name = 'Alert execution error occurred'
    order by start_timestamp desc
  SQL
  time_window  = "1h"
  frequency    = "15m"
  channel_ids  = [logfire_channel.alerts.id]
  notify_when  = "has_matches"
  active       = true
}

resource "logfire_dashboard" "prod_overview" {
  project_id = logfire_project.prod.id
  name       = "production-overview"
  slug       = "production-overview"
  # Export a dashboard JSON from Logfire; its metadata.name is replaced by the value above.
  definition = file("${path.module}/dashboard.json")
}

resource "logfire_write_token" "prod_ingest" {
  project_id = logfire_project.prod.id
}

output "prod_write_token" {
  description = "Write token for sending data to the production project"
  value       = logfire_write_token.prod_ingest.token
  sensitive   = true
}

resource "logfire_read_token" "prod_read" {
  project_id = logfire_project.prod.id
}

output "prod_read_token" {
  description = "Read token for querying the production project"
  value       = logfire_read_token.prod_read.token
  sensitive   = true
}
```

Run `terraform init && terraform apply` to provision Logfire resources. The `examples/` directory holds a runnable copy of this configuration with setup instructions.

## Resources
- `logfire_organization` — manage organizations (self-hosted only; requires a special organization scope) with default-on deletion protection.
- `logfire_project` — manage Logfire projects.
- `logfire_channel` — configure webhook or Opsgenie notification channels (email coming soon).
- `logfire_alert` — define alerting rules tied to channels.
- `logfire_dashboard` — provision dashboards from exported definitions.
- `logfire_write_token` — issue write tokens for ingesting data.
- `logfire_read_token` — issue read tokens for querying projects.

Generated documentation for each resource lives in `docs/` and publishes to the Terraform Registry.

## Developing
```bash
make fmt      # format code
make lint     # static checks (requires golangci-lint)
make test     # unit tests
make testacc  # acceptance tests (requires real credentials)
```
`make build` compiles the provider, while `make install` installs it into your local Go bin for use by Terraform.

## License
MPL-2.0 © Pydantic, Inc.
