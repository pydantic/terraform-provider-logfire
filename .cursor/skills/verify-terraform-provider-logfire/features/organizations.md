# Self-hosted organization lifecycle

Terraform creates and manages organizations in a self-hosted Logfire instance.
Deletion protection requires an explicit configuration change before deletion.

## Sub-features

- `organizations-create` creates an organization with the requested display name.
- `organizations-import` imports the organization and reads its identity.
- `organizations-update` updates the existing organization's display name.
- `organizations-protect` rejects destruction while deletion protection remains enabled.

## How to get to it (user POV)

Use `logfire_organization`. The example is
`examples/self-hosted-organization/main.tf`.

## Driving it with the Terraform test framework

Preconditions: Doctor identifies an owned disposable self-hosted instance.
The API key belongs to its admin-panel organization and holds `organization:admin`.
The instance satisfies the documented organization read/update/delete release floor.

```bash
TF_ACC=1 LOGFIRE_TEST_ORGANIZATIONS=1 TMPDIR="$RUN_DIR/scratch" \
  go test -v -count=1 -timeout=20m -run '^TestAccOrganizationResource$' ./internal/provider/
```

Require creation, import, and the display-name update to pass. Confirm the created
organization is absent after cleanup through the instance organizations API.
For the protection failure path, run the local CLI recipe's
`TestLifecyclePartialCreateRecovery/organization`. It checks protection after a
failed follow-up and recovery. A live protection change additionally needs a
run-owned fixture with protection enabled, a rejected destroy, and confirmation
that the same organization still exists. Disable protection, apply, and destroy
only that fixture after proof.

## Gotchas

- This API is self-hosted only. An API key from a normal organization cannot replace the admin-organization key.
- `LOGFIRE_TEST_ORGANIZATIONS` is an explicit opt-in. A skipped organization test proves nothing about the API.
- The existing live test uses `deletion_protection=false` for cleanup. It does not prove the protected-destroy rejection.
