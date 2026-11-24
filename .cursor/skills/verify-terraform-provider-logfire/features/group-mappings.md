# Self-hosted group mappings

Terraform manages the identity-provider group mapping of a self-hosted instance as
one singleton resource. The API replaces the whole mapping on every write.

## Sub-features

- `group-mappings-create` creates the mapping on an empty instance, adopts a stored mapping that equals the plan, and refuses a different stored mapping or the Helm override.
- `group-mappings-update` replaces the whole mapping, keeps its order, and maps the 400 and 409 answers to errors.
- `group-mappings-import` imports the stored mapping by the fixed ID `instance`.
- `group-mappings-destroy` stores an empty mapping.
- `group-mappings-drift` reads a mapping changed outside Terraform, and warns while the Helm value overrides the API.

## How to get to it (user POV)

Use `logfire_group_mappings`. The example is
`examples/resources/logfire_group_mappings/resource.tf`. The credential is an API key
from the admin organization with `instance:admin` (or `organization:admin`). The routes
are `GET` and `PUT /api/v1/instance/group-mappings/` and take the raw key, with no token exchange.

## Driving it with the Terraform test framework

Preconditions: Launch and the [API-contract review](api-contract.md) are complete. Compare
`GroupOrganizationMappingStatusRead`, `GroupOrganizationMappingUpdate`, and the 400 and 409
answers of both operations.

```bash
TF_ACC= TMPDIR="$RUN_DIR/scratch" go test -v -count=1 -run 'GroupMappings' \
  ./internal/client/ ./internal/provider/
```

Require every `TestLifecycleGroupMappings*` case to appear and pass. They cover create,
no-op plan, import, reorder, drift, destroy sending `[]`, a create refused on a different stored mapping
and on the Helm override, a create that adopts the mapping of a lost response, a 409 on update and destroy, the 400 messages, and an older instance.
The local API checks the raw key and fails the test on a token exchange.

For live acceptance, use a disposable self-hosted instance with an identity provider that
reports group claims, for example Keycloak behind Dex. Apply a mapping for a group, sign in as a member of that group, and confirm
the role in the instance API or UI. Destroy, then read `GET /api/v1/instance/group-mappings/`
and confirm `group_organization_mapping` is `[]`.

## Gotchas

- The API does not check role names. A wrong name applies without error and sign-in skips it.
- Destroy does not restore access that earlier sign-ins changed or removed. Confirm that a live run does not need that access afterward.
- While the Helm value `groupOrganizationMapping` is set, the API refuses writes with a 409. Live acceptance needs an instance without it.
- Mapped organizations and projects must exist. A `logfire_project` is created in the credential's own organization, so a project in another organization must exist by other means.
