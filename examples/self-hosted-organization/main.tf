terraform {
  required_providers {
    logfire = {
      source = "registry.terraform.io/pydantic/logfire"
    }
  }
}

# Configure via env vars:
#   export LOGFIRE_BASE_URL="https://<self-hosted-logfire>"
#   export LOGFIRE_API_KEY="pylf_v1_..."
provider "logfire" {
  base_url = "https://<self-hosted-logfire>"
}

resource "logfire_organization" "example" {
  # Organization CRUD is only available for self-hosted deployments and requires
  # an API key with a special organization scope.
  name         = "terraform-example-org"
  display_name = "Terraform Example Org"

  # The resource defaults to deletion_protection = true.
  # In that default mode, destroy is blocked until you first set this to false
  # and apply. This example uses false to keep apply/destroy loops simple.
  deletion_protection = false
}

output "organization_id" {
  value = logfire_organization.example.id
}
