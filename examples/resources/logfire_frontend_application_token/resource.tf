resource "logfire_frontend_application_token" "current" {
  project_id     = logfire_frontend_application.browser.project_id
  application_id = logfire_frontend_application.browser.id
  adopt_token_id = logfire_frontend_application.browser.token_id
}

# First apply with both resources. Deploy the replacement output everywhere.
# Then remove `current` and apply again to revoke the previous token.
resource "logfire_frontend_application_token" "replacement" {
  project_id     = logfire_frontend_application.browser.project_id
  application_id = logfire_frontend_application.browser.id

  # Before destroying the whole application tree, set this to false and apply.
  # The following destroy can then let application deletion revoke this last token.
  revoke_on_destroy = true
}

output "rotated_browser_write_token" {
  value     = logfire_frontend_application_token.replacement.token
  sensitive = true
}
