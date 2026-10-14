# Credentials and telemetry

Terraform creates API keys and read/write tokens. A usable credential must also
allow its promised operation against the project it belongs to.

## Sub-features

- `credentials-state` preserves create-only secrets through refresh and an empty plan.
- `credentials-replace` replaces keys when expiration changes and updates supported fields in place.
- `credentials-import` imports API-key metadata without claiming to recover its secret.
- `credentials-telemetry` ingests a unique trace and reads it from the intended project.

## How to get to it (user POV)

Use `logfire_api_key`, `logfire_gateway_api_key`, `logfire_read_token`, or
`logfire_write_token`. Sensitive Terraform outputs expose newly created tokens
only to the operator. Logfire MCP provides the independent telemetry view.

## Driving it with Terraform and Logfire MCP

Preconditions: Doctor identifies an authorized disposable instance. The scoped
credential `LOGFIRE_SCOPED_API_KEY` and `LOGFIRE_API_KEY_TEST_PROJECT_ID` identify
the same owned project. They hold every scope the new keys delegate.

```bash
TF_ACC=1 TMPDIR="$RUN_DIR/scratch" go test -v -count=1 -timeout=20m \
  -run '^TestAcc(APIKeyResource|GatewayAPIKeyResource|ReadTokenResource|WriteTokenResource)$' \
  ./internal/provider/
```

Require all four tests to run. Save state assertions, empty-plan results,
replacement actions, and supported imports. These tests destroy their fixtures.
For token usability, keep a separate run-owned Terraform fixture alive long enough
to send one OTLP trace with its created write credential. Record a random trace ID
and `service.name=terraform-provider-verification`.

Connect Logfire MCP to that same instance with a provisioned project-scoped
`logfire_api_key` that holds `project:read`. Use that API key as the bearer for
`/mcp`. The operator credential must be allowed to delegate `project:read`.
Read its tool schema, confirm project identity, and call its schema reference
tool. Query `records` for the exact trace ID with an explicit time range.
Current MCP calls this tool `query_run`; discover the deployed name and arguments.
Require the returned trace ID, service name, and span name to match the sent span.
Save the endpoint identity, SQL, time range, and rows without the credentials.

If a read token changes, exercise that token through `/v2/query` over HTTP.
Do not use an SDK read token as the `/mcp` bearer. An MCP query using a different
credential does not prove the read token works. Destroy
the Terraform fixture after proof and confirm the project and keys are absent.

## Gotchas

- Missing scoped credentials skip API-key tests. A passing command with those skips is incomplete.
- Read/write tokens do not support import. Imported API keys cannot recover the original create-only secret.
- MCP uses `project:read`. SDK read tokens use `project:read_otlp` for the HTTP query API.
- Gateway key creation does not prove a paid provider request or spend enforcement. Do not make external model calls without authorization.
