# Flag email addresses on every endpoint.
resource "logfire_gateway_prebuilt_guardrail" "email_addresses" {
  template_slug = "pii-email"

  targets = [
    { action = "flag" },
  ]
}

# Redact credit card numbers on the support endpoint, and block them on the billing endpoint.
resource "logfire_gateway_prebuilt_guardrail" "credit_cards" {
  template_slug = "financial-credit-card"

  targets = [
    {
      endpoint_slug = logfire_gateway_endpoint.support.slug
      action        = "redact"
    },
    {
      endpoint_slug = logfire_gateway_endpoint.billing.slug
      action        = "block"
    },
  ]
}
