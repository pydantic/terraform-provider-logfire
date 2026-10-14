---
name: verify-terraform-provider-logfire
description: Verify terraform-provider-logfire changes against platform's public API schema and existing Terraform resource lifecycle tests. Use before claiming provider behavior works.
---

# Verify the Logfire Terraform provider

Read [the feature map](features/README.md) and select the entries affected by the
change. Compare API-backed changes with the relevant platform `api.json`. Reuse
the existing Go and Terraform tests for resource creation, read, update, import,
replacement, drift, and deletion. Local CLI tests use temporary HTTP APIs.
Use live acceptance when the changed behavior needs a running API. Do not require
a cloud deployment or telemetry query for every provider change.

## Launch

Run from the checkout being changed. The provider has no server to keep running.
The test framework starts the provider and owns each temporary API and state file.
Create a private run directory outside the repository:

```bash
umask 077
export RUN_DIR=$(mktemp -d "${TMPDIR:-/tmp}/terraform-logfire-verify.XXXXXX")
mkdir -p "$RUN_DIR/tools" "$RUN_DIR/scratch" "$RUN_DIR/evidence"
git rev-parse HEAD > "$RUN_DIR/evidence/source-sha.txt"
git diff --binary > "$RUN_DIR/evidence/source.diff"
```

Check `go version` against `go.mod`. Check for `make`, `curl`, `unzip`, and
`shasum` before the download below. Install missing tools before starting a check.
Terraform must be at least 1.8. CI's Terraform matrix contains 1.8 and 1.14.
Use an existing supported binary through `TF_ACC_TERRAFORM_PATH` and put its
directory on `PATH`. Or download this task-local binary from HashiCorp and verify
its published checksum:

```bash
terraform_version=1.14.9
terraform_os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
  arm64|aarch64) terraform_arch=arm64 ;;
  x86_64|amd64) terraform_arch=amd64 ;;
  *) printf 'Unsupported Terraform architecture\n' >&2; exit 1 ;;
esac
terraform_archive="terraform_${terraform_version}_${terraform_os}_${terraform_arch}.zip"
terraform_release="https://releases.hashicorp.com/terraform/$terraform_version"
curl -fSs "$terraform_release/$terraform_archive" -o "$RUN_DIR/tools/$terraform_archive" &&
curl -fSs "$terraform_release/terraform_${terraform_version}_SHA256SUMS" -o "$RUN_DIR/tools/SHA256SUMS" &&
(
  cd "$RUN_DIR/tools" &&
  awk -v archive="$terraform_archive" '$2 == archive' SHA256SUMS > selected.sha256 &&
  test -s selected.sha256 &&
  shasum -a 256 -c selected.sha256 &&
  cp selected.sha256 "$RUN_DIR/evidence/terraform-archive.sha256" &&
  unzip -q "$terraform_archive"
) || exit 1
export TF_ACC_TERRAFORM_PATH="$RUN_DIR/tools/terraform"
export PATH="$RUN_DIR/tools:$PATH"
"$TF_ACC_TERRAFORM_PATH" version -json > "$RUN_DIR/evidence/terraform-version.json"
go version > "$RUN_DIR/evidence/go-version.txt"
go mod download
go build ./...
```

For optional live acceptance, set `LOGFIRE_BASE_URL` and `LOGFIRE_API_KEY` for the
authorized disposable instance. Check the required scopes and release floors
in [the provider documentation](../../../docs/index.md). Set
`LOGFIRE_EXPECTED_ORG` to that organization's name. Do not select a hosted account
through inherited credentials or inferred API-key regions.

## Doctor

Run this read-only check before driving the CLI and whenever a result looks wrong:

```bash
git rev-parse HEAD
"$TF_ACC_TERRAFORM_PATH" version
terraform version
go version
git status --short
```

Require the intended source revision. Both Terraform commands must select the
same supported executable so that generation and tests use the same version. Save
the initial diff and untracked source file hashes when the checkout has changes.
For API-backed work, select [the platform API contract](features/api-contract.md).
Record the selected commit, schema hash, and the compatibility claim it supports.
The repository does not select a platform revision automatically. If the relevant
revision is unclear, report that gap and continue the local checks.
For live checks, confirm the selected URL, organization, project, credential
scopes, and reported Logfire release through its public API or UI. Record identity
without recording credentials. Stop if the target cannot be identified.

## Drive

The default is the API-contract review and existing credential-free tests. Run
the checks in `CONTRIBUTING.md` for implementation changes:

```bash
make generate
make lint
TF_ACC= TMPDIR="$RUN_DIR/scratch" make test
```

`make generate` formats examples and regenerates Registry documentation from the
Go provider schema. It does not generate an API client from platform OpenAPI.
Review its diff. `make test` includes client request, provider protocol, and local
Terraform CLI tests. For a focused uncached lifecycle run, use:

```bash
TF_ACC= TMPDIR="$RUN_DIR/scratch" make test-e2e
```

This invokes real Terraform commands against temporary local HTTP APIs. The tests
check resource state, import, recovery, request counts, and deletion. They do not
contact a Logfire deployment or validate its responses against `api.json`.
Compare affected HTTP fixtures and client fields with the selected schema.
Run matching [feature recipes](features/README.md). Use their live acceptance
sections only when runtime API behavior remains uncertain or the task requests it.
Capture every command, stdout, stderr, and exit code. Require the selected test
names to appear in the output.
Record each `SKIP` with its missing prerequisite.

The existing `.github/workflows/test.yml` runs same-repository live acceptance on
Terraform 1.8 and 1.14 when credentials are available.
`.github/workflows/nightly-acceptance.yml` also runs live SaaS acceptance. Missing
secrets or scoped credentials can skip tests. Those results do not prove the
skipped behavior.

Run the applicable hooks from `.pre-commit-config.yaml`. Do not use `make install`
to change the operator's global provider installation for verification.

## Evidence

Store a receipt in `$RUN_DIR/evidence/` with the source revision and diff, tool
versions, feature IDs, exact commands, exit codes, and test results. Label each
proof as schema review, local HTTP API, real self-hosted API, or real hosted API.
Record which resource actions the existing tests exercise and any missing action.

For live resource changes, include the before and after values, resource IDs,
import equivalence, no-op plan result, and deletion evidence. For an explicit
telemetry check, save the trace ID and the MCP query and matching rows.
A successful apply, token creation, or empty query result alone is insufficient.

Terraform state and acceptance logs can contain secrets. Keep them private.
Create redacted receipts before sharing evidence. Never commit state, generated
CLI credentials, token outputs, or real organization data.

## Cleanup

Go test cleanup closes its temporary APIs. Terraform test cleanup destroys its
resources. After a live failure, use the saved IDs and surviving private state to
remove only resources owned by this run. Confirm their absence through the API.
Do not assume a passing test has checked remote deletion.

Remove the run's tools and scratch files after all needed cleanup. Preserve proof:

```bash
rm -rf -- "$RUN_DIR/tools" "$RUN_DIR/scratch"
test -s "$RUN_DIR/evidence/source-sha.txt"
test -d "$RUN_DIR/evidence"
```

Stop only processes started by this run. Do not remove a shared stack. Record
unremoved resources as incomplete cleanup. Use pstack's
`maintain-verification-skill` when the test commands or provider behavior change.

## Helpers

No new helper is needed. `make test`, `make test-e2e`, the existing `TestAcc` tests,
and Terraform's test framework own the CLI lifecycle.
