resource "logfire_project" "example" {
  name = "example-project"
}

resource "logfire_read_token" "example" {
  project_id = logfire_project.example.id
  expires_at = "2099-12-31T23:59:59Z"
}
