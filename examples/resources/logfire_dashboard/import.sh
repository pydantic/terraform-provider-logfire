# Set project_id, name, and slug to the existing dashboard's values.
# Save its exported JSON to dashboard.json, then import by project name and slug.
terraform import logfire_dashboard.example "example-project/example-dashboard"

# A matching configuration and export produce no changes, with exit code 0.
terraform plan -detailed-exitcode
