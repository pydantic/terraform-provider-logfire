# Local Terraform CLI lifecycle

Terraform creates, updates, imports, and deletes provider resources through the
normal provider protocol. Local APIs simulate failures at the HTTP boundary.

## Sub-features

- `local-channel` preserves Slack settings through updates and import, then deletes the channel.
- `local-recovery` retains partial creation state and recovers without duplicate resources.
- `local-failure` sends one create request after HTTP 429, HTTP 503, or a lost response.
- `local-validation` rejects a null dashboard and resolves project import IDs correctly.

## How to get to it (user POV)

Use `terraform apply`, `terraform import`, and `terraform destroy` with this provider.
The existing Terraform test framework runs these commands with private state.

## Driving it with the Terraform test framework

Preconditions: Complete Launch and Doctor. Use a supported Terraform executable.
This recipe requires no Logfire credentials or running stack.

```bash
TF_ACC= TMPDIR="$RUN_DIR/scratch" make test-e2e
```

Require `TestLifecycleSlackSettings`, `TestLifecyclePartialCreateRecovery`,
`TestLifecycleFailedCreates`, `TestLifecycleDashboardNullDefinition`, and
`TestLifecycleProjectImportIDCollision` to pass. Save the transcript and exit code.
The assertions check state values, import results, one create per resource,
and one final deletion for channel and organization recovery.

## Gotchas

- `TF_ACC=` deliberately excludes live acceptance. These results do not prove a real Logfire API contract.
- `TF_ACC_TERRAFORM_PATH` selects the binary. The Terraform executable on `PATH` can be an older version.
- This command already includes `-count=1`. A cached result is not a new verification run.
