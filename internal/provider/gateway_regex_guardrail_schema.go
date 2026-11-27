// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// Schema defines the supported regex guardrail configuration.
func (r *GatewayRegexGuardrailResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an AI Gateway guardrail that matches a custom regular expression.\n\n" +
			gatewayGuardrailBetaNote + "\n\n" + gatewayGuardrailScopesNote +
			" Regex, Presidio, and prebuilt guardrails share one organization-unique name space. Import accepts the guardrail UUID or its name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Regex guardrail UUID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization-unique guardrail name. At most 255 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Guardrail description. At most 1000 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 1000)},
			},
			"pattern": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Regular expression that the Gateway matches against request messages. At most 1000 characters. Logfire refuses a pattern that the Gateway engine cannot compile.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 1000)},
			},
			"flags": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
				MarkdownDescription: "Regular expression flags, any of `g`, `i`, `m`, and `s`. Defaults to no flags.",
				Validators: []validator.String{
					stringvalidator.LengthAtMost(8),
					stringvalidator.RegexMatches(regexp.MustCompile(`^[gims]*$`), "must contain only the flags g, i, m, and s"),
				},
			},
			"targets":         gatewayGuardrailTargetsAttribute(),
			"sample_messages": gatewayGuardrailSampleMessagesAttribute(50, 20000),
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the guardrail was created.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig checks the target rules that the API enforces on the whole set.
func (r *GatewayRegexGuardrailResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	validateGatewayGuardrailTargets(ctx, req.Config, &resp.Diagnostics)
}
