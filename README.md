# Terraform Provider for Logfire

This provider lets you manage [Pydantic Logfire](https://pydantic.dev/logfire) projects, alerting channels, and alert policies through Terraform. It targets the registry address `registry.terraform.io/pydantic/logfire`.

## Requirements
- Terraform CLI 1.5 or newer.
- A Logfire account and API key.

## Quick Start
```hcl
terraform {
  required_providers {
    logfire = {
      source  = "registry.terraform.io/pydantic/logfire"
      version = "~> 0.1"
    }
  }
}

provider "logfire" {
  # You can also rely on LOGFIRE_BASE_URL and LOGFIRE_API_KEY env vars.
  base_url = "https://logfire-us.pydantic.dev"
  api_key  = "pylf_v1_..."
}

resource "logfire_project" "prod" {
  organization = "my-org"
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
  organization = logfire_project.prod.organization
  project      = logfire_project.prod.name
  name         = "execution-errors"
  query        = "select * from records where span_name = 'Alert execution error occurred'"
  time_window  = "24h"
  frequency    = "6h"
  channel_ids  = [logfire_channel.alerts.id]
  notify_when  = "has_matches"
  active       = true
}
```

Run `terraform init && terraform apply` to provision Logfire resources. The `examples/` directory holds a runnable copy of this configuration with setup instructions.

## Resources
- `logfire_project` — manage Logfire projects.
- `logfire_channel` — configure webhook, email, or Opsgenie notification channels.
- `logfire_alert` — define alerting rules tied to channels.

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
