resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_frontend_application" "browser" {
  project_id        = logfire_project.example.id
  name              = "browser"
  service_namespace = "storefront"
  environment       = "production"

  lifecycle {
    # A namespace-only replacement cannot overlap the old unique identity.
    # Use a separately named application when that handoff must avoid interruption.
    create_before_destroy = true
  }
}

output "browser_write_token" {
  value     = logfire_frontend_application.browser.token
  sensitive = true
}
