# Managed Logfire resources

Terraform manages project settings, notification channels, dashboards, alerts,
and SLOs. Verify the resource actions affected by the change.

## Sub-features

- `resources-project` creates a project, clears its description, renames it, and changes visibility.
- `resources-import` imports supported resources by ID or name and checks the resulting state.
- `resources-update` updates channels, dashboard definitions, alerts, and SLO configuration.
- `resources-drift` checks an unchanged configuration produces an empty plan.

## How to get to it (user POV)

Use the `logfire_project`, `logfire_channel`, `logfire_dashboard`, `logfire_alert`,
and `logfire_slo` resources. Their examples live under `examples/resources/`.

## Driving it with the Terraform test framework

Preconditions: Launch and the [API-contract review](api-contract.md) are complete.

Use the [shared Drive checks](../SKILL.md#drive) for the existing tests.
Compare the affected create, read, update, import, no-op, and delete expectations
with the selected schema. Record any action that has no existing local coverage.

Use live acceptance when the change needs runtime API evidence or the task
requests it. Doctor then identifies the authorized disposable Logfire organization.
`LOGFIRE_API_KEY`, `LOGFIRE_BASE_URL`, and `LOGFIRE_EXPECTED_ORG` select it explicitly.
The credential has the documented scopes. SLOs are available in the selected instance.

```bash
TF_ACC=1 TMPDIR="$RUN_DIR/scratch" go test -v -count=1 -timeout=20m \
  -run '^TestAcc(ProjectResource|ChannelResource|DashboardResource|AlertResource|SloResource)$' \
  ./internal/provider/
```

Save each selected test result. Require the created values, updated values,
supported import comparisons, and existing empty-plan assertions to pass.
For a deletion change, retain the run-owned IDs before cleanup and independently
confirm their absence through the API after destroy.

## Gotchas

- The tests create real resources. Never use an unselected production organization.
- Creating configuration does not prove alert delivery or dashboard query results. Those checks apply when the task changes that behavior.
- A skipped SLO or missing API is incomplete coverage. Match the instance release to the feature under test.
- A `CheckDestroy` comment does not establish that a test contains a remote deletion check.
