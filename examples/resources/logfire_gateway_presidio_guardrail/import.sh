# Import an existing Presidio guardrail by its organization-unique name, or by its UUID.
#
# By name:
terraform import 'logfire_gateway_presidio_guardrail.email_addresses' "email-addresses"

# By UUID:
terraform import 'logfire_gateway_presidio_guardrail.email_addresses' "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed66"
