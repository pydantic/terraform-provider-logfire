resource "logfire_gateway_presidio_guardrail" "email_addresses" {
  name          = "email-addresses"
  connection_id = logfire_gateway_guardrail_connection.presidio.id
  on_error      = "allow"
  entity_type   = "EMAIL_ADDRESS"
  language      = "en"

  operator = {
    type      = "replace"
    new_value = "<EMAIL>"
  }
  score_threshold = 0.6

  targets = [
    { action = "redact" },
  ]
}
