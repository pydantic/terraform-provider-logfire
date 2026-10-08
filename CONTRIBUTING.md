# Contributing

This is the source provider. Pulumi and Crossplane releases are generated from
released versions of this repo.

## Local Checks

```bash
make fmt
make generate
make verify
```

`make verify` checks formatting, builds the provider, runs the Go tests with the
race detector, and runs the linters. The Go suite includes Terraform CLI tests
against local APIs, so it needs no Logfire credentials. Install Terraform
locally or set `TF_ACC_TERRAFORM_PATH` to avoid repeated downloads.

`make test-e2e` runs the lifecycle and failure tests with the existing Terraform
test framework. These tests cover Slack settings, partial creation recovery,
and create requests after lost responses or rate limits. CI runs them on
Terraform 1.8 and 1.14. Extend these and the live acceptance tests for provider
changes; keep unit tests for behavior a CLI flow cannot reach.

`make generate` refreshes the Terraform Registry docs in `docs/`. Commit those
docs whenever schema, examples, descriptions, or validators change.

## API Changes

For API-backed changes, keep the API models, Terraform schema, docs, validators,
and tests in sync.

Acceptance tests use real Logfire resources:

```bash
TF_ACC=1 LOGFIRE_API_KEY="pylf_v2_..." make testacc
```

## Versioning

The provider follows semantic versioning. Before 1.0, a breaking change bumps
the minor version. Every other change bumps the patch. From 1.0, a breaking
change bumps the major version, a feature bumps the minor version, and a fix
bumps the patch.

A breaking change needs a changelog entry. The entry names what breaks and
gives the migration steps. The GitHub release notes repeat them.

Keep a deprecated attribute or resource for at least one minor release. Set the
schema `Deprecated` marker. Add the changelog entry in the release that
deprecates it, and name the release that removes it.

## Release

Move the `Unreleased` changelog section under the new version. Tag `main` with
`vX.Y.Z`, push the tag, and verify the GitHub release plus the Terraform
Registry version. Then update Pulumi and Crossplane from that release.
