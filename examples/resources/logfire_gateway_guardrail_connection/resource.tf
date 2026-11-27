variable "presidio_token" {
  type      = string
  sensitive = true
}

resource "logfire_gateway_guardrail_connection" "presidio" {
  name         = "presidio"
  base_url     = "https://presidio.example.com"
  auth_type    = "bearer"
  bearer_token = var.presidio_token
  timeout_ms   = 3000
}
