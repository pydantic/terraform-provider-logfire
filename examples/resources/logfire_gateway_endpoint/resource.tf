resource "logfire_gateway_endpoint" "default" {
  slug        = "default"
  description = "Prefer OpenAI and fall back to Anthropic."

  providers = [
    {
      provider_id = logfire_gateway_provider.openai.id
      priority    = 10
    },
    {
      provider_id = logfire_gateway_provider.anthropic.id
    },
  ]
}
