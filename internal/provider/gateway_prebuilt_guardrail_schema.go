// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// Schema defines the supported prebuilt guardrail configuration.
func (r *GatewayPrebuiltGuardrailResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	templateComputed := func(description string) schema.StringAttribute {
		return schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: description,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the targets of an AI Gateway prebuilt guardrail, which applies a Logfire guardrail template such as a PII detector.\n\n" + gatewayGuardrailBetaNote + "\n\n" +
			gatewayGuardrailScopesNote + " The organization has one configuration per template. Create adopts a configuration that already exists and replaces its targets. Import accepts the template slug.",
		Attributes: map[string]schema.Attribute{
			"id": templateComputed("Template slug of the configured guardrail."),
			"template_slug": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Slug of the Logfire guardrail template. Logfire validates the slug. Changing it replaces the resource.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 128)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"targets":         gatewayGuardrailTargetsAttribute(),
			"sample_messages": gatewayGuardrailSampleMessagesAttribute(20, 4000),
			"name":            templateComputed("Template name."),
			"description":     templateComputed("Template description."),
			"category":        templateComputed("Template category."),
		},
	}
}

// ValidateConfig checks the target rules that the API enforces on the whole set.
func (r *GatewayPrebuiltGuardrailResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	validateGatewayGuardrailTargets(ctx, req.Config, &resp.Diagnostics)
}
