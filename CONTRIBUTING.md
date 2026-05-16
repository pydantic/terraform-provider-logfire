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

For API-backed changes, check `~/Pydantic/platform/api.json` first. Keep the API
models, Terraform schema, docs, validators, and tests in sync.

Acceptance tests use real Logfire resources:

```bash
TF_ACC=1 LOGFIRE_API_KEY="pylf_v2_..." make testacc
```

## Release

Tag `main` with `vX.Y.Z`, push the tag, and verify the GitHub release plus the
Terraform Registry version. Then update Pulumi and Crossplane from that release.
