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
  description = "Instance-admin API key created in the admin organization, with the instance:admin scope."
}

provider "logfire" {
  base_url = var.base_url
  api_key  = var.api_key
}

resource "logfire_organization" "team_a" {
  name                = "team-a"
  deletion_protection = false
}

# One alias per organization. Referencing the organization resource makes
# Terraform destroy what the alias manages before it destroys the organization.
provider "logfire" {
  alias        = "team_a"
  base_url     = var.base_url
  api_key      = var.api_key
  organization = logfire_organization.team_a.name
}

resource "logfire_project" "api" {
  provider = logfire.team_a
  name     = "api"
}

resource "logfire_write_token" "api" {
  provider   = logfire.team_a
  project_id = logfire_project.api.id
}
