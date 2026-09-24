# Contributing

This is the source provider. Pulumi and Crossplane releases are generated from
released versions of this repo.

## Local Checks

```bash
make fmt
make generate
make lint
make test
```

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

Tag `main` with `vX.Y.Z`, push the tag, and verify the GitHub release plus the
Terraform Registry version. Then update Pulumi and Crossplane from that release.
