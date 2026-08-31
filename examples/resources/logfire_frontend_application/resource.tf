resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_frontend_application" "browser" {
  project_id        = logfire_project.example.id
  name              = "browser"
  service_namespace = "storefront"
  environment       = "production"
}

output "browser_write_token" {
  value     = logfire_frontend_application.browser.token
  sensitive = true
}
