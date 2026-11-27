# Import an existing guardrail connection by its organization-unique name, or by its UUID.
#
# By name:
terraform import 'logfire_gateway_guardrail_connection.presidio' "presidio"

# By UUID:
terraform import 'logfire_gateway_guardrail_connection.presidio' "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed44"
