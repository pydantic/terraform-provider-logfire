# Examples

This directory contains runnable Terraform examples for the Logfire provider.

## Prerequisites

- Terraform CLI `>= 1.8`
- A [Logfire account](https://pydantic.dev/logfire) and API key

## Available examples

- `main.tf`: SaaS-compatible example (project, channel, alert, dashboard, and one read/write token).
- `self-hosted-organization/main.tf`: self-hosted only example for `logfire_organization` (requires a special organization scope).

## Running the SaaS-compatible example

Set credentials in your shell:

```bash
export LOGFIRE_API_KEY="pylf_v1_..."
```

The provider infers the SaaS endpoint from the API key region. Self-hosted deployments should set `LOGFIRE_BASE_URL` explicitly.

Then run:

```bash
cd examples
terraform init
terraform apply
```

### Token expiration E2E in `main.tf`

The example provisions one read token and one write token with no expiration by default.
To test expiration, set `expires_at` on either token resource to an RFC3339 timestamp (for example `2099-12-31T23:59:59Z`).

Inspect results after apply:

```bash
terraform state show logfire_write_token.production_ingest
terraform state show logfire_read_token.production_read
```

## Running the self-hosted organization example

```bash
cd examples/self-hosted-organization
terraform init
terraform apply
```

## Cleanup

Destroy the resources when you finish experimenting:

```bash
terraform destroy
```
