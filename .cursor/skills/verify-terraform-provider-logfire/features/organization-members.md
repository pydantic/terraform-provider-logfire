# Organization members

Terraform sets the role of an existing organization member and lists the members.
The public API cannot add a member, so the resource adopts a user who is already one.
Destroying the resource removes the user from the organization.

## Sub-features

- `members-adopt` finds the member by `user_id`, changes the role only when it differs, and fails with the ways in when the user is not a member.
- `members-update` changes the role of the member. A custom role read back from the UI shows as a change to the built-in role. The email and name are read for information and change nothing.
- `members-replace` replaces the resource when `user_id` or `organization` changes. The old user leaves the old organization first, and the plan does not check the create.
- `members-delete` removes the user from the organization and treats only the "Organization member not found" answer as already gone and fails on any other 404.
- `members-import` imports by user ID, or `<organization>/<user_id>` through the exchange, and refuses an ID that is not a lowercase UUID.
- `members-errors` maps the 400, 402, 403 and 422 answers of the member routes to messages that include the platform's detail. The 403 message names the missing `organization:write_member` scope as well as role escalation.
- `members-data-source` lists `id`, `email`, `name` and `role`, ordered by email address. It is how a configuration gets a user ID from an email address.
- `members-organization` acts in the organization that the resource, the data source or the provider names and records its ID in private state. A resource argument wins over the provider. Create and import fail before any change when the provider cannot confirm the organization: a key without `organization:read` and no `organization`. A rename is followed, a deleted organization drops the member on refresh and destroy succeeds, and a provider or key that names another organization fails.

## How to get to it (user POV)

Use `logfire_organization_member` and the `logfire_organization_members` data source.
The examples are under `examples/resources/logfire_organization_member/` and
`examples/data-sources/logfire_organization_members/`. They act on the organization
of the provider credential, which needs `organization:read_member` and
`organization:write_member`, unless `organization` selects another one through an
instance-admin key (see [Organization context](organization-context.md)).

## Driving it with the Terraform test framework

Preconditions: Launch and the [API-contract review](api-contract.md) are complete.
Compare `GET /api/v1/members/`, `PATCH /api/v1/members/{user_id}/` and
`DELETE /api/v1/members/{user_id}/` with the selected `api.json`, and read the route
code in `logfire_backend/routes/api/v1/members.py` for the error codes. The schema
lists only 204, 422 and 429, so the 400, 402, 403 and 404 answers come from the route code.

```bash
TF_ACC= TMPDIR="$RUN_DIR/scratch" go test -v -count=1 -run 'OrganizationMember' ./internal/provider/
```

Require every test named `TestLifecycleOrganizationMember*` and `TestLifecycleOrganizationMembersDataSource*` to pass. The fake API mimics the
routes, so this is local HTTP API evidence only.

Live acceptance needs a disposable self-hosted organization with two users who are
already members and a key that holds both member scopes. Do not use an organization
that a group mapping manages. Record the members and roles with the data source, apply a
configuration that changes one member's role, confirm the role through the UI or the
members API, and import the member in a second state. Destroy removes the user, so
use a user that the fixture can add again.

## Gotchas

- Terraform removes the user from the organization on destroy, including a user who was a member before the resource existed.
- A group mapping resets the role at each sign-in. The resource then shows drift after every sign-in.
- The PATCH route accepts only `admin`, `member`, `guest` and `billing`. A custom role is never written.
- The route has no pagination. The provider lists every member for each read.
- The exchanged token has no user, so the route code treats it as a service caller: `caller_role_from_token` gives it `has_all_permissions`, and `check_role_not_escalating` returns before comparing roles. The 403 escalation answer cannot occur through an organization. The 400, 402 and 422 answers still apply.
- The members API does not return the organization's name, so a key that cannot read its own organization cannot confirm it, and the provider refuses it.
