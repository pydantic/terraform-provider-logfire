# Organization context

Terraform acts in any organization of a self-hosted Logfire instance from one
instance-admin API key. The provider exchanges the key for a short-lived token
for the organization.

## Sub-features

- `context-provider` sends an organization token on every organization request when the provider sets `organization` or `LOGFIRE_ORGANIZATION`, and keeps the key on `/api/v1/instance/` routes and when no organization is set.
- `context-exchange` shares one exchange per organization among concurrent requests, replaces a rejected token once, retries a create's exchange, drops `expires_in` when the instance caps the lifetime, and reports a missing token endpoint or a key without `instance:admin` by name.
- `context-project` creates, imports, updates, and destroys a `logfire_project` in the organization it names, and records `organization_id`.
- `context-follow` replaces a project when `organization` names another existing organization, updates it in place for a rename, for an unknown name, and for a name the instance does not list yet (a failed update deletes nothing), updates `organization` on refresh after a rename, and drops the project from state after the organization is deleted.

## How to get to it (user POV)

Set `organization` on the `provider "logfire"` block or on `logfire_project`. The
example is `examples/self-hosted-organization-context/main.tf`.

## Driving it with the Terraform test framework

Preconditions: Launch and the [API-contract review](api-contract.md) are complete.
Compare the scope set in `internal/client/token_exchange.go` with the scopes of
each organization-scoped route in the selected `api.json`.

```bash
TF_ACC= TMPDIR="$RUN_DIR/scratch" go test -v -count=1 \
  -run 'TestOrganizationClient|TestConcurrentRequests|TestRejectedToken|TestCreateWaits|TestExchange|TestLifecycleProject|TestProject(Organization|Plan)' \
  ./internal/client/ ./internal/provider/
```

The lifecycle tests run a real Terraform CLI against a local API with one project
store per organization ID. The local API can rename and delete an organization
between steps. Require one create and one delete per project and no request in
another organization.

For live acceptance, use an owned disposable self-hosted instance. The API key
belongs to the admin organization and holds `instance:admin`. Set `base_url` to
the exact base URL the instance accepts as the exchange audience. That is its
frontend host, or the backend's `OIDC_AUDIENCE` when it is set.

1. Apply the example with two organizations, each through its own provider alias. Require a project in each and an empty second plan.
2. Rename one organization through the API. Require refresh to update the project's `organization`, an empty plan, and the same project ID.
3. Set `organization` on the project to the new name, and require no replacement.
4. Delete an organization through the API. Require refresh to drop its project from state.

## Gotchas

- The exchange works only on self-hosted instances from release v2026-06-25.01. The local API does not check scopes against `api.json`.
- `invalid_target` covers an unknown organization and an audience that does not match the instance.
- The Terraform test framework serves every provider block from one provider process, and the last configured block wins. A lifecycle test with two provider blocks of different organizations acts in one of them only. Use one provider block per configuration and set `organization` on the projects.
- The token endpoint limits exchanges to 20 a minute and 100 an hour per client IP.
- A `LOGFIRE_ORGANIZATION` in the environment applies to every local test whose provider block omits `organization`. The lifecycle tests clear it.
- The exchanged token has no user, so `logfire_api_key` and `logfire_gateway_api_key` fail through it.
