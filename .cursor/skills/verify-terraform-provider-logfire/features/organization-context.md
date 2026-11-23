# Organization context

Terraform acts in any organization of a self-hosted Logfire instance from one
instance-admin API key. The provider exchanges the key for a short-lived token
bound to the organization.

## Sub-features

- `context-provider` sends an organization-bound token on every organization-scoped request when the provider sets `organization` or `LOGFIRE_ORGANIZATION`.
- `context-project` creates, reads, updates, imports, and deletes a `logfire_project` in the organization it names, and replaces the project when `organization` changes.
- `context-routes` keeps the raw key on `/api/v1/instance/` routes and on every request when no organization is set.
- `context-state` records the organization ID and name of each resource in private state, for a key of its own organization too when the key can read its organization (`organization:read`). A retargeted provider or a key of another organization fails for the resources of the old organization. A key of the recorded organization proves it by reading its own organization, so it needs no `instance:admin`. A project follows a renamed organization, and a provider that keeps the old name fails. A project that sets no organization fails the plan when the provider now acts in another one. A project whose `organization` is unknown at plan time fails the plan. Refresh drops the resources of a deleted organization and destroy succeeds. A project created with a key that cannot read its organization moves to an instance-admin key through the exchange fallback, and a fallback that fails for a reason other than refusing the key keeps the project in state.
- `context-transport` refuses a redirect of any request to another host or from HTTPS to HTTP, shares one exchange or organization list among concurrent callers without tying them to the first caller's context, replaces a rejected token once, and drops the cached tokens and list when `logfire_organization` creates, renames, or deletes an organization.
- `context-exchange` shares one exchange among concurrent requests, retries without `expires_in` when the instance caps the lifetime lower, refuses a redirect to another host, and reports a missing token endpoint as an old instance instead of a missing resource.
- `context-errors` shows the audience on `invalid_target`, names the instance-admin requirement when the exchange or the organization list refuses the key, and rejects organization names outside the instance rule, such as `team-a/`.

## How to get to it (user POV)

Set `organization` on the `provider "logfire"` block or on `logfire_project`. The
example is `examples/self-hosted-organization-context/main.tf`.

## Driving it with the Terraform test framework

Preconditions: Launch and the [API-contract review](api-contract.md) are complete.
Compare the scope set in `internal/client/token_exchange.go` with the scopes of
each organization-scoped route in the selected `api.json`. The gateway routes
accept `organization:read` and `organization:write`.

Run the credential-free client, provider, and lifecycle checks:

```bash
TF_ACC= TMPDIR="$RUN_DIR/scratch" go test -v -count=1 \
  -run 'TestOrganizationClient|TestExchange|TestConcurrentRequestsShareOneExchange|TestOrganizationContext|TestProviderOrganization|TestProviderRejects(Unknown|Malformed)Organization|TestClientFor|TestSaveOrganizationContext|TestWithExchangeFallback|TestProject(Read|Delete|Import)|TestImport|TestCredentialedRequests|TestSameHostRedirect|TestCancelledCaller|TestRelist|TestForgetOrganization|TestRejectedToken|TestOwnOrganization|TestIsExchangeRefusal|TestSaveOrganizationContext|TestLifecycle(ProjectOrganizationContext|RetargetedProvider|ProviderFollowsRenamed|ProjectOfRenamed|ResourcesOfDeleted|ProjectMovesToInstanceAdminKey|KeySwitch|ExchangeRecorded|OwnKeyThatCannotRead|ProjectSurvives|UnknownOrganization|ProjectWithoutOrganization|ProjectCreateFails)' \
  ./internal/client/ ./internal/provider/
```

Require `TestLifecycleProjectOrganizationContext` to pass all seven cases. They
run a real Terraform CLI against a local API with one store per organization ID.
The cases cover a project that sets `organization`, a provider that sets it, a
project that overrides the provider, no organization at all, a replacement after
the organization changes, a name with a path, and an organization that
disappears. The first case applies, refreshes through the exchange, updates,
imports, and destroys, and requires one create, one delete, and no request in
the wrong organization.

The other lifecycle tests cover `context-state`. They switch a state between a key of its own organization and an instance-admin key in both directions, and the local API answers `GET /api/v1/organization/` for each key unless the test removes `organization:read`. The local API in
`organization_instance_test.go` can rename and delete an organization between
steps. Require the retargeted provider to leave the Gateway spending limits of
`team-b` at their own value, the renamed project to be deleted once in its
renamed organization, and the deleted organization to leave an empty state.

The client tests pin the exchange form, the scope set, the token cache, the retry
after a 401, the shared exchange, the `expires_in` retry, and the redirect rule.

Use live acceptance when runtime exchange behavior needs proof or the task requests
it. Doctor identifies an owned disposable self-hosted instance, for example a Kind
cluster that runs the Logfire Helm chart. The API key belongs to the admin
organization and holds `instance:admin`. The chart's CI values create such a key.
Set `base_url` to the exact base URL the instance accepts as the exchange audience.
That is its frontend host, or the backend's `OIDC_AUDIENCE` setting when it is
set. For a cluster reached through a port forward, set `OIDC_AUDIENCE` on the
backend to the forwarded URL, such as `http://127.0.0.1:18731`.

1. Apply `examples/self-hosted-organization-context` with `-var base_url=... -var api_key=...`. Require the organization, both projects, and the write token.
2. Exchange the key by hand for `team-a` with `scope=project:read` and list `/api/v1/projects/` with the token. Require both projects there. A key that holds only `instance:admin` gets 403 on the same list. Keep the key and the tokens out of logs and the checkout.
3. Run `terraform plan` again and require an empty plan. Change a project description and require an in-place update.
4. Run `terraform state rm` on one project, then `terraform import` it as `team-a/<name>`. Require the same attributes and an empty plan.
5. Create a `logfire_api_key` through the provider alias. The instance answers `403 This credential cannot create API keys` for any scopes, because the exchanged token has no user. Record the answer.
6. Destroy. Confirm through `/api/v1/instance/organizations/` that `team-a` is gone.

For `context-state`, run a configuration with a provider alias that sets
`organization` to a literal name and manages a project, a write token, and
`logfire_gateway_spending_limits` when the instance serves the Gateway.

1. Change the alias to another existing organization. Require refresh and destroy to fail with `lives in organization`, and the limits of the other organization to stay unchanged.
2. Rename the organization with `PUT /api/v1/organization/` and an exchanged token that holds `organization:write`. Require a plan that keeps the old name to fail, and a plan with the new name to be empty.
3. Delete the organization outside Terraform with `DELETE /api/v1/organization/`. Require refresh to drop its resources and destroy to succeed. `terraform destroy -refresh=false` exercises the delete path of each resource. A `logfire_organization` defaults to `deletion_protection = true`, so its own destroy fails until you set it to false.
4. Rename a `logfire_organization` whose `name` a project and the alias reference. Require a plain plan to fail before any change, `terraform apply -target` on the organization to rename it, and the next plan to be empty.

The error for a key from a normal organization is covered by the local tests only.

## Gotchas

- The exchange works only on self-hosted instances from release v2026-06-25.01. The local API does not check scopes against `api.json`.
- `invalid_target` covers an unknown organization and an audience that does not match the instance. Its description tells them apart.
- The `organization_context` private state key holds `{"id": ..., "name": ...}`. A state without it belongs to a key that cannot read its organization, or was written before the record existed, and adopts the organization that the provider or the project names. Tools that drop private state lose the record, and the provider cannot then tell a retargeted provider from the original one.
- Every provider process reads the organization of its key once with `GET /api/v1/organization/` and the key itself. A local API that does not model the route must answer 404, as an older instance does.
- The Terraform test framework serves every provider block from one provider process, and the last configured block wins. A lifecycle test with two provider blocks of different organizations acts in one of them only. Use one provider block per configuration.
- The token endpoint limits exchanges to 20 a minute and 100 an hour per client IP. Repeated live runs from one host can reach the hourly limit.
- After an organization is deleted, `terraform plan -refresh-only` fails with `Unknown Logfire organization` when an alias takes its `organization` from `logfire_organization`. A normal plan or destroy works.
- A `LOGFIRE_ORGANIZATION` in the environment applies to every local test whose provider block omits `organization`. `TestLifecycleProjectOrganizationContext` clears it. Unset it before running the other tests.
- The exchanged token has no user. A project created through it has no extra member.
