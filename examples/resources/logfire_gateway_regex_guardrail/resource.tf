resource "logfire_gateway_regex_guardrail" "order_ids" {
  name        = "order-ids"
  description = "Internal order numbers."
  pattern     = "ORD-[0-9]{6}"
  flags       = "i"

  targets = [
    {
      endpoint_slug = logfire_gateway_endpoint.support.slug
      action        = "redact"
    },
  ]

  sample_messages = [
    {
      role    = "user"
      content = "Where is my order ORD-123456?"
    },
  ]
}
