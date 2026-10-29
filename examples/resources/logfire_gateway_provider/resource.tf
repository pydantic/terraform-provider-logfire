variable "openai_api_key" {
  type      = string
  sensitive = true
}

variable "bedrock_api_key" {
  type      = string
  sensitive = true
}

variable "azure_api_key" {
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

resource "logfire_gateway_provider" "azure" {
  slug     = "primary-azure"
  vendor   = "azure"
  api_key  = var.azure_api_key
  endpoint = "https://my-resource.openai.azure.com"
}

resource "logfire_gateway_provider" "vertex" {
  slug     = "primary-vertex"
  vendor   = "google-vertex"
  api_key  = file("vertex-service-account.json")
  endpoint = "https://us-central1-aiplatform.googleapis.com"
}
