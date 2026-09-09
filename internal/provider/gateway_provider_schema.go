// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Schema defines the supported public Gateway provider configuration.
func (r *GatewayProviderResource) Schema(
	ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an organization-owned AI Gateway provider. " +
			"Requires Growth, Enterprise Cloud, or self-hosted and a token with " +
			"organization:read and organization:write scopes. " +
			"Built-in providers and custom upstream URLs are not supported.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Provider UUID. Use this identifier to import an existing provider.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"slug": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization-unique provider slug. Changing it replaces the provider.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 64),
					stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`),
						"must start with a letter or digit and contain only letters, digits, dots, underscores, or hyphens"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"vendor": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Upstream vendor: openai, anthropic, bedrock, doubleword, groq, huggingface, or mistral. " +
					"Changing it replaces the provider.",
				Validators: []validator.String{
					stringvalidator.OneOf("openai", "anthropic", "bedrock", "doubleword", "groq", "huggingface", "mistral"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"api_key": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "Upstream API key. Changing it rotates the credential in place. " +
					"Logfire never returns this value; Terraform retains it in state. " +
					"External credential changes cannot be detected. After import, the next apply sets the configured key.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"require_pricing": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Require known model pricing before forwarding requests. Defaults to true.",
			},
			"bedrock_api": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Bedrock API variant: runtime or mantle. Required only for bedrock; can be updated in place.",
				Validators:          []validator.String{stringvalidator.OneOf("runtime", "mantle")},
			},
			"bedrock_region": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "AWS region used to derive the Bedrock endpoint, for example us-east-1. " +
					"Required only for bedrock; can be updated in place.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(3, 63),
					stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+$`),
						"must be a lowercase AWS region such as us-east-1"),
				},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the provider was created.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig checks that only Bedrock providers select an API and region.
func (r *GatewayProviderResource) ValidateConfig(
	ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse,
) {
	var config GatewayProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.Vendor.IsUnknown() || config.Vendor.IsNull() {
		return
	}
	for name, value := range map[string]types.String{
		"bedrock_api":    config.BedrockAPI,
		"bedrock_region": config.BedrockRegion,
	} {
		if value.IsUnknown() {
			continue
		}
		if config.Vendor.ValueString() == "bedrock" && value.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Missing Bedrock configuration",
				name+" is required when vendor is bedrock.")
		}
		if config.Vendor.ValueString() != "bedrock" && !value.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unexpected Bedrock configuration",
				name+" can only be set when vendor is bedrock.")
		}
	}
}
