# Import an existing Gateway endpoint by its organization-unique slug, or by its UUID.
#
# By slug:
terraform import 'logfire_gateway_endpoint.default' "default"

# By UUID:
terraform import 'logfire_gateway_endpoint.default' "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed00"
