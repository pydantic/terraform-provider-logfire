## Unreleased

FEATURES:
- Add experimental `logfire_frontend_application` and `logfire_frontend_application_token` resources for immutable browser telemetry identities, recoverable restricted tokens, import, and explicit two-phase token rotation. The backing management API is currently enabled only in Logfire staging environments.
- Add `slack-integration` notification channels, posting via the organization's installed Logfire Slack App instead of an incoming-webhook URL. The config takes an `install_id` (the Slack App installation created by connecting Slack in the Logfire UI) and a Slack `channel_id` the bot is a member of, plus an optional `include_agent_prompt` flag for issue notifications. No secret is stored in Terraform state; the backend resolves the installation's bot token at send time.
- Add `histogram_threshold` support to the `logfire_slo` resource: the `metric_aggregation` enum now accepts `histogram_threshold` (a metrics-only bucket-ratio SLI such as "95% of observations under 60s"), and two new optional attributes `threshold` and `comparison` carry the cutoff and its good side. `bad_query` is now optional and unused for that mode; every other mode still requires it. The pairing is validated at plan time, mirroring the API. Requires a Logfire backend that accepts histogram-threshold SLOs.
- Add PagerDuty notification channels with a sensitive Events API v2 `routing_key` and optional `us` or `eu` `region`.
- Add `pagerduty-integration` notification channels, paging through the organization's installed Logfire PagerDuty App instead of a per-channel Events API v2 routing key. The `logfire_pagerduty_service` data source resolves PagerDuty's account subdomain and external service ID to the internal Logfire `install_id` and `service_id` required by the channel. No secret is stored in Terraform state; the backend resolves the service's integration key at send time. The existing `pagerduty` type is unchanged and keeps working.
- Add optional `page_channel_ids` / `ticket_channel_ids` attributes to `logfire_slo` to seed the generated burn-rate alerts' notification channels at creation. Delivery stays alert-owned afterwards; the attributes have no effect on an already-created SLO. Requires a Logfire backend that accepts channel seeding on the public SLO API.
- Add an experimental `logfire_slo` resource for managing Service Level Objectives. The backing Logfire API is not yet stable, so the resource schema and behavior may change in backwards-incompatible ways.
- Add an optional `environments` attribute (set of strings) to the `logfire_alert` resource to scope the alert query to specific deployment environments. Omitting it (or setting it empty) keeps the current behavior of evaluating against all environments.

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
