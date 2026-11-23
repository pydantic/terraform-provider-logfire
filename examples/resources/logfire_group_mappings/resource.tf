# Map identity-provider groups to organization and project roles. The provider
# credential must be an API key from the admin organization with the
# `instance:admin` scope. Do not set the Helm value `groupOrganizationMapping`
# on the same instance.
resource "logfire_organization" "team_a" {
  name = "team-a"
}

resource "logfire_group_mappings" "this" {
  mapping = [
    {
      group_id = "platform-ops"
      organization_roles = [
        {
          organization = logfire_organization.team_a.name
          role         = "guest"
          # The project must already exist in the organization.
          project_roles = [
            { project = "api", role = "read" },
          ]
        },
      ]
    },
    {
      group_id = "team-a-admins"
      organization_roles = [
        { organization = logfire_organization.team_a.name, role = "admin" },
      ]
    },
  ]
}
