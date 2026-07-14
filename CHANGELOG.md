## Unreleased

FEATURES:
- Add an experimental `logfire_slo` resource for managing Service Level Objectives. The backing Logfire API is not yet stable, so the resource schema and behavior may change in backwards-incompatible ways.

## 0.1.4

BUG FIXES:
- Treat empty resource IDs as absent during `Read`, which fixes Upjet/Crossplane observe-create flows.
- Tolerate parent-project deletion when reading or deleting project-scoped child resources like tokens, alerts, and dashboards.

## 0.1.3

BUG FIXES:
- Preserve configured webhook URLs and Opsgenie auth keys in Terraform state when the Logfire API returns masked channel credentials on create, read, and update.
- Ignore `config.url` during channel import verification because the Logfire API redacts webhook URLs on read.

## 0.1.0

FEATURES:
- Initial release of the Terraform provider for Pydantic Logfire.
- Manage projects, alert channels (webhook and Opsgenie), alerts, dashboards, and read/write tokens.
- Provider configuration via `base_url` and `api_key` fields or `LOGFIRE_BASE_URL`/`LOGFIRE_API_KEY` environment variables.
