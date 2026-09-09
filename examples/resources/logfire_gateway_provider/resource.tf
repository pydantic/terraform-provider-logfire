variable "openai_api_key" {
  type      = string
  sensitive = true
}

variable "bedrock_api_key" {
  type      = string
  sensitive = true
}

resource "logfire_gateway_provider" "openai" {
  slug    = "primary-openai"
  vendor  = "openai"
  api_key = var.openai_api_key
}

resource "logfire_gateway_provider" "bedrock" {
  slug            = "primary-bedrock"
  vendor          = "bedrock"
  api_key         = var.bedrock_api_key
  bedrock_api     = "runtime"
  bedrock_region  = "us-east-1"
  require_pricing = false
}
