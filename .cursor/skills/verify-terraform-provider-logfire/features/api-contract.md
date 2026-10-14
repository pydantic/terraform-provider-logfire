# Platform API and Terraform schema

The provider's HTTP routes, JSON fields, response handling, and scopes must match
the relevant public Logfire API. Its Terraform schema also controls generated docs.

## Sub-features

- `api-routes` compares affected HTTP methods and paths with the public API.
- `api-payloads` compares request fields, response fields, nullability, defaults, and errors.
- `api-scopes` checks the credential scopes required by the affected operations.
- `api-terraform` keeps provider schema, examples, validators, and generated docs consistent.

## How to get to it (user POV)

Terraform resource actions call the public Logfire API. The platform checkout
tracks its schema at `src/services/logfire-backend/logfire_backend/tests/openapi/api.json`.
A running instance serves it at `/api/openapi.json`.

## Driving it with Git and the existing checks

Preconditions: Launch is complete. Set `PLATFORM_REPO` to the relevant platform
checkout and `PLATFORM_REV` to the revision selected for this task's compatibility
claim. This recipe records that selection. It does not define a provider-wide pin.

```bash
: "${PLATFORM_REPO:?Select the platform checkout}"
: "${PLATFORM_REV:?Select the platform revision for this task}"
api_schema=src/services/logfire-backend/logfire_backend/tests/openapi/api.json
platform_sha=$(git -C "$PLATFORM_REPO" rev-parse "$PLATFORM_REV^{commit}") &&
printf '%s\n' "$platform_sha" > "$RUN_DIR/evidence/platform-sha.txt" &&
git -C "$PLATFORM_REPO" show "$platform_sha:$api_schema" > "$RUN_DIR/evidence/api.json" &&
shasum -a 256 "$RUN_DIR/evidence/api.json" > "$RUN_DIR/evidence/api.sha256" || exit 1
```

Read the affected operations and referenced component schemas. The schema's
`servers` prefix is `/api`. For resource paths such as `/v1/projects/`, the Go
client uses `/api/v1/projects/`. Compare them with `internal/client/`, the affected
provider resource, and existing request and lifecycle test fixtures. Record the methods, fields,
scopes, and resource actions checked. `info.version` does not identify a platform release.

Run `make generate`, `make lint`, and `TF_ACC= make test` as the main skill states.
Review the generated documentation diff. Use `make test-e2e` for the focused
Terraform CLI lifecycle checks. Both test commands use existing tests.

## Gotchas

- The client models are handwritten. The provider has no API client generator or provider-wide platform version pin.
- `make generate` uses the Go provider schema through `tfplugindocs`. It does not compare Go models with platform OpenAPI.
- Do not assume every local HTTP fixture validates against OpenAPI. Identify the enrolled operations and recorded versions in the client tests. Compare uncovered operations separately.
- Schema shape does not prove route availability, authorization, or applied fields. `TestSloDeliveryOnOlderLogfire` checks backends that accept but ignore `alerts`.
- An untracked schema copy without a known source revision cannot establish release compatibility. Record the gap and continue the local checks.
