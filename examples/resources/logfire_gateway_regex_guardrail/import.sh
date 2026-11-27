# Import an existing regex guardrail by its organization-unique name, or by its UUID.
#
# By name:
terraform import 'logfire_gateway_regex_guardrail.order_ids' "order-ids"

# By UUID:
terraform import 'logfire_gateway_regex_guardrail.order_ids' "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed55"
