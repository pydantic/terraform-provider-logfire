# Provider verification map

Read the [skill](../SKILL.md) first. Use its private run directory and doctor check.
Select every entry affected by the diff. Capture the command and resulting state.
Report unrun recipes and skipped tests separately from passing checks.

| Feature | Recipe | Boundary |
| --- | --- | --- |
| Terraform lifecycle, import and failure recovery | [Local CLI lifecycle](local-cli.md) | Real Terraform CLI, temporary HTTP API |
| Projects, dashboards, channels, alerts and SLOs | [Managed resources](managed-resources.md) | Real Logfire API |
| API keys, read/write tokens and telemetry access | [Credentials and telemetry](credentials.md) | Real Logfire API and MCP query |
| Self-hosted organizations | [Organization lifecycle](organizations.md) | Real self-hosted admin API |

Gateway provider protocol checks and PagerDuty lookup tests do not prove external
provider connectivity. Frontend application acceptance requires the staging-only
API and its explicit opt-in. Record these limits when a change touches those paths.
