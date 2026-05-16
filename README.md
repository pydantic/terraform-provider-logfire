# Terraform Provider for Logfire

Manage [Pydantic Logfire](https://pydantic.dev/logfire) projects, alerting, dashboards, and API tokens with Terraform. The provider is published at `registry.terraform.io/pydantic/logfire`.

## Requirements
- Terraform CLI 1.8 or newer
- A Logfire API key (`api_key` argument or `LOGFIRE_API_KEY`)
- A Logfire base URL only for self-hosted deployments (`base_url` argument or `LOGFIRE_BASE_URL`)

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
  # Or set LOGFIRE_API_KEY in the environment.
  # base_url is inferred automatically for Logfire SaaS tokens.
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
    # Also supports "opsgenie" (with `auth_key`).
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
  # Optional RFC3339 timestamp.
  # expires_at = "2099-12-31T23:59:59Z"
}

output "prod_write_token" {
  description = "Write token for the production project"
  value       = logfire_write_token.prod_ingest.token
  sensitive   = true
}

resource "logfire_read_token" "prod_read" {
  project_id = logfire_project.prod.id
  # Optional RFC3339 timestamp.
  # expires_at = "2099-12-31T23:59:59Z"
}

output "prod_read_token" {
  description = "Read token for the production project"
  value       = logfire_read_token.prod_read.token
  sensitive   = true
}
```

Run `terraform init && terraform apply` to provision Logfire resources.

For Logfire SaaS, the provider infers the API endpoint from the region embedded in the API key and uses `https://logfire-us.pydantic.dev` or `https://logfire-eu.pydantic.dev` automatically. If you set `base_url` or `LOGFIRE_BASE_URL`, that value is used instead. Self-hosted customers should always set `base_url` explicitly.

## Examples
- `examples/main.tf` contains a SaaS-compatible end-to-end setup (project, channel, alert, dashboard, and read/write tokens).
- `examples/self-hosted-organization/main.tf` contains the self-hosted-only `logfire_organization` example.

## Resources
- `logfire_organization` — manage organizations (self-hosted only; requires a special organization scope) with default-on deletion protection.
- `logfire_project` — manage Logfire projects.
- `logfire_channel` — configure webhook or Opsgenie notification channels.
- `logfire_alert` — define alerting rules tied to channels.
- `logfire_dashboard` — provision dashboards from exported definitions.
- `logfire_write_token` — issue write tokens for ingesting data.
- `logfire_read_token` — issue read tokens for querying projects.

Generated documentation for each resource lives in `docs/` and publishes to the Terraform Registry.

## Developing
See [CONTRIBUTING.md](CONTRIBUTING.md) for the full local workflow, generated
documentation rules, acceptance test setup, and release checklist.

```bash
make fmt      # format code
make lint     # static checks (requires golangci-lint)
make test     # unit tests
make testacc  # acceptance tests (requires real credentials)
```
`make build` compiles the provider, while `make install` installs it into your local Go bin for use by Terraform.

For local Terraform CLI testing against an unpublished build, use Terraform's
official `dev_overrides` workflow in a temporary CLI config file:

```hcl
provider_installation {
  dev_overrides {
    "pydantic/logfire" = "/path/to/local/provider-dir"
  }
  direct {}
}
```

Then point Terraform at that file for the shell session, for example
`TF_CLI_CONFIG_FILE=/tmp/logfire-dev.tfrc terraform apply`. See the HashiCorp
docs for `dev_overrides` in the CLI configuration file for details.

## License
MPL-2.0 © Pydantic, Inc.
