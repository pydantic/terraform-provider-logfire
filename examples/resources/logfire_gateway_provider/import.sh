# Import an existing Gateway provider by its UUID, or by its organization-unique slug.
#
# The UUID comes from the provider list endpoint:
#   curl -s -H "Authorization: Bearer $LOGFIRE_API_KEY" \
#     "$LOGFIRE_BASE_URL/api/v1/gateway/providers/" | jq '.providers[] | {id, slug}'
#
# By slug:
terraform import 'logfire_gateway_provider.openai' "openai"

# By UUID:
terraform import 'logfire_gateway_provider.openai' "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed11"
