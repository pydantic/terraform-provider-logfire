## 0.1.3

BUG FIXES:
- Preserve configured webhook URLs and Opsgenie auth keys in Terraform state when the Logfire API returns masked channel credentials on create, read, and update.
- Ignore `config.url` during channel import verification because the Logfire API redacts webhook URLs on read.

## 0.1.0

FEATURES:
- Initial release of the Terraform provider for Pydantic Logfire.
- Manage projects, alert channels (webhook and Opsgenie), alerts, dashboards, and read/write tokens.
- Provider configuration via `base_url` and `api_key` fields or `LOGFIRE_BASE_URL`/`LOGFIRE_API_KEY` environment variables.
