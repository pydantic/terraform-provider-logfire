resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_dashboard" "example" {
  project_id = logfire_project.example.id
  name       = "example-dashboard"
  slug       = "example-dashboard"
  definition = file("${path.module}/dashboard.json")
}
