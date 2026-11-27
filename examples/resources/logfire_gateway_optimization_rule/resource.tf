resource "logfire_gateway_optimization_rule" "concise_support" {
  name        = "Concise support answers"
  description = "Keep answers on the support endpoint short."
  category    = "verbosity"
  instruction = "Answer in at most three sentences unless the user asks for detail."

  targets = [
    {
      endpoint_slug = logfire_gateway_endpoint.default.slug
    },
    {
      endpoint_slug = logfire_gateway_endpoint.default.slug
      model_pattern = "^gpt-5"
      agent_name    = "support-triage"
    },
  ]
}
