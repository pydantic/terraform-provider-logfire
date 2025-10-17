# Examples

This directory contains an example on how to setup the logfire provider and create some resources.

## Prerequisites

- Terraform CLI `>= 1.5`
- A [Logfire account](https://pydantic.dev/logfire) and API key


## Running the example

Make sure to update the ```api_key``` and ```base_url``` with your account specific values
```bash
cd examples
terraform init
terraform apply
```

## Cleanup

Destroy the resources when you finish experimenting:

```bash
terraform destroy
```
