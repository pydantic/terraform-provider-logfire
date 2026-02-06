# Examples

This directory contains examples for setting up the Logfire provider and creating resources.

## Prerequisites

- Terraform CLI `>= 1.5`
- A [Logfire account](https://pydantic.dev/logfire) and API key


## Available examples

- `main.tf`: SaaS-compatible example (project/channel/alert/dashboard/token resources).
- `self-hosted-organization/main.tf`: self-hosted only example for `logfire_organization` (requires a special organization scope).

## Running the SaaS-compatible example

Make sure to update the `api_key` and `base_url` with your account specific values.
```bash
cd examples
terraform init
terraform apply
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
