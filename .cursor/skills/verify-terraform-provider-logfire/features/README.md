# Provider verification map

Read the [skill](../SKILL.md) first. Use its private run directory and doctor check.
Select every entry affected by the diff. Capture the command and resulting state.
Report unrun recipes and skipped tests separately from passing checks.
API-contract review and the local tests are the default. Use the optional live
sections for API behavior that the local checks cannot settle or when requested.

| Feature | Recipe | Boundary |
| --- | --- | --- |
| API routes, payloads, scopes and generated Terraform docs | [API contract](api-contract.md) | Selected platform schema and provider schema |
| Terraform lifecycle, import and failure recovery | [Local CLI lifecycle](local-cli.md) | Real Terraform CLI, temporary HTTP API |
| Projects, dashboards, channels, alerts and SLOs | [Managed resources](managed-resources.md) | Local tests, optional real API acceptance |
| API keys, read/write tokens and telemetry access | [Credentials and telemetry](credentials.md) | Local tests, optional real API and MCP query |
| Self-hosted organizations | [Organization lifecycle](organizations.md) | Local tests, optional real admin API |

Gateway provider protocol checks and PagerDuty lookup tests do not prove external
provider connectivity. Frontend application acceptance requires the staging-only
API and its explicit opt-in. Record these limits when a change touches those paths.
