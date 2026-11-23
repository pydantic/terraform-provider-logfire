terraform {
  required_version = ">= 1.8"

  required_providers {
    logfire = {
      source = "registry.terraform.io/pydantic/logfire"
    }
  }
}

variable "base_url" {
  type        = string
  description = "Base URL of the self-hosted Logfire instance."
}

variable "api_key" {
  type        = string
  sensitive   = true
  description = "Instance-admin API key created in the admin organization, with the instance:admin or organization:admin scope."
}

# One instance-admin key reaches every organization. Without an organization,
# this provider acts in the organization of the key.
provider "logfire" {
  base_url = var.base_url
  api_key  = var.api_key
}

resource "logfire_organization" "team_a" {
  name                = "team-a"
  display_name        = "Team A"
  deletion_protection = false
}

# Terraform cannot use for_each on provider blocks, so projects take the
# organization as an argument.
resource "logfire_project" "svc" {
  for_each     = toset(["api", "worker"])
  organization = logfire_organization.team_a.name
  name         = each.key
}

# Resources that take only a project ID use a provider alias. Referencing the
# organization resource makes Terraform destroy these resources before it
# destroys the organization.
provider "logfire" {
  alias        = "team_a"
  base_url     = var.base_url
  api_key      = var.api_key
  organization = logfire_organization.team_a.name
}

resource "logfire_write_token" "api" {
  provider   = logfire.team_a
  project_id = logfire_project.svc["api"].id
}

output "api_write_token" {
  description = "Write token for the api project in team-a"
  value       = logfire_write_token.api.token
  sensitive   = true
}
