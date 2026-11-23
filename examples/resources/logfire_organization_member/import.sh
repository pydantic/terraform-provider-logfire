# The import ID is the user ID.
terraform import 'logfire_organization_member.alice' "0191a5c0-0000-7000-8000-00000000a11c"

# For a member of another organization, prefix the organization name.
terraform import 'logfire_organization_member.alice' "team-a/0191a5c0-0000-7000-8000-00000000a11c"
