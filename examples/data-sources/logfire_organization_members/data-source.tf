data "logfire_organization_members" "all" {}

output "admin_emails" {
  value = [for m in data.logfire_organization_members.all.members : m.email if m.role == "admin"]
}

# On a self-hosted instance, an instance-admin key lists another organization.
data "logfire_organization_members" "team_a" {
  organization = "team-a"
}

# The user ID of a member, found by email address, for logfire_organization_member.
output "alice_user_id" {
  value = one([for m in data.logfire_organization_members.team_a.members : m.id if lower(m.email) == "alice@example.com"])
}
