# The user must already be a member of the organization. Terraform sets the role.
data "logfire_organization_members" "all" {}

resource "logfire_organization_member" "alice" {
  user_id = one([for m in data.logfire_organization_members.all.members : m.id if lower(m.email) == "alice@example.com"])
  role    = "admin"
}

# On a self-hosted instance, an instance-admin key acts in another organization.
data "logfire_organization_members" "team_a" {
  organization = "team-a"
}

resource "logfire_organization_member" "bob" {
  organization = "team-a"
  user_id      = one([for m in data.logfire_organization_members.team_a.members : m.id if lower(m.email) == "bob@example.com"])
  role         = "guest"
}
