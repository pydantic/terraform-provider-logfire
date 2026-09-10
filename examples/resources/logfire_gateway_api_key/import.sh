# Import an existing gateway API key by its UUID. Key names are not unique
# across the organization, so the import is UUID-only. The UUID comes from the
# API key list endpoint (the API never returns the plaintext token again, so an
# import recovers the key without its token):
#   curl -s -H "Authorization: Bearer $LOGFIRE_API_KEY" \
#     "$LOGFIRE_BASE_URL/api/v1/api-keys/" | jq '.[] | {id, name}'
terraform import 'logfire_gateway_api_key.example' "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"
