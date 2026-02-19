resource "logfire_organization" "example" {
  # Organization CRUD is only available for self-hosted deployments and requires
  # an API key with a special organization scope.
  name         = "terraform-example-org"
  display_name = "Terraform Example Org"

  # This defaults to true. Set false to allow destroy.
  deletion_protection = false
}
