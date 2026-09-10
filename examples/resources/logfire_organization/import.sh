# Import an existing organization by name or UUID. The provider credential must be
# an API key from the admin organization carrying the `organization:admin` scope.
#
# By name:
terraform import 'logfire_organization.example' "terraform-example-org"

# By UUID:
terraform import 'logfire_organization.example' "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"
