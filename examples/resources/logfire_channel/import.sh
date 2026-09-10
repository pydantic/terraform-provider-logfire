# Import an existing channel by its UUID, or by its name (label).
#
# The UUID comes from the channel list endpoint:
#   curl -s -H "Authorization: Bearer $LOGFIRE_API_KEY" \
#     "$LOGFIRE_BASE_URL/api/v1/channels/" | jq '.[] | {id, label}'
#
# By name:
terraform import 'logfire_channel.example' "alerts-webhook"

# By UUID:
terraform import 'logfire_channel.example' "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"
