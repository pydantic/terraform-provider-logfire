## Unreleased

## 0.2.0

BREAKING CHANGES:

These changes need Logfire v2026-09-23.01 or newer, which sets SLO channels per tier alert and serves the schedules API. Do not upgrade the provider before your Logfire instance has that release (self-hosted: Helm chart `logfire-0.13.47` or newer). On an older release, an SLO create or update that sets `alerts.<tier>.channel_assignments` fails with an error that names this release: the older release ignores `alerts` on SLO writes, so the SLO is saved but its alerts do not notify the configured channels. `logfire_alert.channel_assignments` also works on older releases.

- `logfire_alert`: `channel_ids` is replaced by `channel_assignments`, a set of `{ channel_id, schedule_id }` objects. `schedule_id` is optional. It limits the channel to the windows of a `logfire_schedule`. The provider sends and reads the value 1:1 with the alert API's `channel_assignments`.
- `logfire_slo`: `page_channel_ids` and `ticket_channel_ids` are removed. They are replaced by the `alerts` attribute, keyed by burn-rate tier (`fast`, `medium`, `slow`). Each tier has a writable `channel_assignments`, with the same type as `logfire_alert.channel_assignments`, and the computed `alert_id`, `severity` and `viable` of that tier's alert. Delivery is set per tier, as in the Logfire API: the old attributes set both page alerts at once.
- `logfire_slo`: a change to the channels of an existing SLO now changes its alerts. Before, `page_channel_ids` and `ticket_channel_ids` were sent only at creation: a later change reported success and changed only the Terraform state, so alerts could stay without the configured channels. The provider now sends a configured tier's assignments when they differ from what that alert has, and reads every tier back from its alert. A change made on the Logfire alerts page therefore shows as drift in the next plan, and the next apply writes the configured value back.
- `logfire_slo`: a tier that is not configured is not sent and not managed. The provider reports its current channels in state and never changes them, so you can manage only some tiers without a perpetual diff. To remove every channel of a tier, set its `channel_assignments` to `[]`.

Migration:

1. Upgrade Logfire first, then the provider. No state change or import is necessary: the provider drops the old attributes from state and reads `alerts` from the API on the next refresh.
2. On `logfire_alert`, replace `channel_ids = [a, b]` with `channel_assignments = [{ channel_id = a }, { channel_id = b }]`.
3. On `logfire_slo`, replace `page_channel_ids = [a]` and `ticket_channel_ids = [b]` with:

   ```hcl
   alerts = {
     fast   = { channel_assignments = [{ channel_id = a }] }
     medium = { channel_assignments = [{ channel_id = a }] }
     slow   = { channel_assignments = [{ channel_id = b }] }
   }
   ```

   Put a list that several tiers share in a `local`.
4. Run `terraform plan`. If an SLO's alerts do not have the configured channels (for example because a channel was added to the configuration after the SLO was created), the plan shows an update of that SLO. Apply it to route the alerts.
5. If you worked around the old behaviour by replacing the SLO on every channel change (for example `replace_triggered_by`, or `replaceOnChanges` in Pulumi), remove the workaround. A replacement deletes the SLO's error-budget history.

FEATURES:
- Add the `logfire_schedule` resource for organization-level delivery schedules (`label`, `timezone`, and `windows` of ISO weekdays with `HH:MM` or `HH:MM:SS` start and end times), with import by UUID (the API has no schedule list). Use a schedule as `schedule_id` in a channel assignment on `logfire_alert` or `logfire_slo`. The provider credential needs `organization:read_channel` / `organization:write_channel`.
- `logfire_slo.alerts` reports each tier alert's `viable` flag (false when the tier cannot fire at the target, so the alert is kept but not evaluated). An SLO created before Logfire kept all three tier alerts can have a tier with a null `alert_id`. The provider then plans an update of the SLO, even with no attribute change, and that update creates the missing alert.
- Add `logfire_api_key` resource for unified API keys with any scopes: multi-scope OTLP keys (`project:read_otlp` + `project:write_otlp`), org-wide keys, gateway keys with spend caps, and management keys. Name, description, and gateway claims update in place; scopes, project, and expiry replace the key. The provider credential needs `organization:create_api_key` plus every scope it delegates.
- Add `logfire_gateway_api_key` resource for project-scoped AI Gateway keys (`project:gateway_proxy` scope) with spend caps and cache settings. Not to be confused with `logfire_gateway_provider`, which configures an upstream LLM provider credential.
- Document `logfire_write_token` and `logfire_read_token` as single-scope conveniences over `logfire_api_key`; their behavior is unchanged.
- Add experimental `logfire_frontend_application` and `logfire_frontend_application_token` resources for immutable browser telemetry identities, recoverable restricted tokens, import, and explicit two-phase token rotation. The backing management API is currently enabled only in Logfire staging environments.
- Add `slack-integration` notification channels, posting via the organization's installed Logfire Slack App instead of an incoming-webhook URL. The config takes an `install_id` (the Slack App installation created by connecting Slack in the Logfire UI) and a Slack `channel_id` the bot is a member of, plus an optional `include_agent_prompt` flag for issue notifications. No secret is stored in Terraform state; the backend resolves the installation's bot token at send time.
- Add `histogram_threshold` support to the `logfire_slo` resource: the `metric_aggregation` enum now accepts `histogram_threshold` (a metrics-only bucket-ratio SLI such as "95% of observations under 60s"), and two new optional attributes `threshold` and `comparison` carry the cutoff and its good side. `bad_query` is now optional and unused for that mode; every other mode still requires it. The pairing is validated at plan time, mirroring the API. Requires a Logfire backend that accepts histogram-threshold SLOs.
- Add PagerDuty notification channels with a sensitive Events API v2 `routing_key` and optional `us` or `eu` `region`.
- Add `pagerduty-integration` notification channels, paging through the organization's installed Logfire PagerDuty App instead of a per-channel Events API v2 routing key. The `logfire_pagerduty_service` data source resolves PagerDuty's account subdomain and external service ID to the internal Logfire `install_id` and `service_id` required by the channel. No secret is stored in Terraform state; the backend resolves the service's integration key at send time. The existing `pagerduty` type is unchanged and keeps working.
- Add optional `page_channel_ids` / `ticket_channel_ids` attributes to `logfire_slo` to seed the generated burn-rate alerts' notification channels at creation. Delivery stays alert-owned afterwards; the attributes have no effect on an already-created SLO. Requires a Logfire backend that accepts channel seeding on the public SLO API. Replaced by the per-tier `alerts` attribute (see BREAKING CHANGES).
- Add an experimental `logfire_slo` resource for managing Service Level Objectives. The backing Logfire API is not yet stable, so the resource schema and behavior may change in backwards-incompatible ways.
- Add an optional `environments` attribute (set of strings) to the `logfire_alert` resource to scope the alert query to specific deployment environments. Omitting it (or setting it empty) keeps the current behavior of evaluating against all environments.
- `logfire_gateway_provider` import now accepts the organization-unique slug in addition to the provider UUID, so an import does not require fetching the UUID from the provider list endpoint first.
- Collection endpoints for newer APIs (API keys, Gateway providers, instance organizations) now turn a missing-route 404 into an actionable message naming the minimum Logfire release (and Helm chart where known), plus the instance release when it reports one. The release comes from the `Logfire-Version` response header; a build that reports only an image identity is treated as unknown rather than quoted as a version. The provider docs gained a self-hosted compatibility table listing the minimum release per feature.
- The provider docs gained a "Required scopes" section: a per-resource scope table, the delegation and project-access rules, the two-credential pattern for gateway keys, and fixes for the common credential errors.
- `logfire_channel` import now accepts the channel name (label) in addition to the channel UUID, matching the name-based imports of projects and organizations.
- Document import for `logfire_api_key` and `logfire_gateway_api_key` (UUID, with the list endpoint that exposes it), and state that `logfire_read_token` and `logfire_write_token` do not support import because the API never returns the token again after creation.
- Organization creation and listing now use the `/api/v1/instance/organizations/` endpoints, and organization read, update, and delete use the `/api/v1/organization/` org-context endpoints, authenticating each with a short-lived organization-scoped token exchanged from the provider's admin key at `/api/oauth/token` (RFC 8693); all legacy `/api/v1/organizations/` routes are deprecated server-side. The provider key needs no new scope: `organization:admin` satisfies the exchange, and the exchanged token carries only `organization:read`/`organization:write` for the target organization. Creating and listing organizations require a Logfire backend from 2026-06-03 (v2026-06-03.01) or newer; every other operation, including setting `billing_email` at creation (which performs an org-context update), requires 2026-06-25 (v2026-06-25.01) or newer.

DOCUMENTATION:
- Update the scope guidance for the delegation change that lets an organization-wide key hold and pass on project-bound scopes (`project:gateway_proxy`, `project:read_otlp`, `project:write_otlp`) when it also holds `organization:create_api_key`, so a single organization-wide credential can create gateway keys for any project on Logfire v2026-09-14.01 or newer. The gateway-keys section, scope table, and troubleshooting note now cover both that shape and the project-scoped operator alternative.
- Name the `organization:admin` scope requirement on `logfire_organization` explicitly (created in the admin organization), replacing the vague "special organization scope" wording, and add import instructions by organization name or UUID.

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
