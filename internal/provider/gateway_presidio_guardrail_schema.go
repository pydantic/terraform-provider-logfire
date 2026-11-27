// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// Schema defines the supported Presidio guardrail configuration.
func (r *GatewayPresidioGuardrailResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an AI Gateway guardrail that detects one entity type through a Presidio `logfire_gateway_guardrail_connection`.\n\n" + gatewayGuardrailBetaNote + "\n\n" +
			gatewayGuardrailScopesNote + " Creating or changing the guardrail also needs a plan with Presidio guardrails (Enterprise Cloud or self-hosted). Regex, Presidio, and prebuilt guardrails share one organization-unique name space. Import accepts the guardrail UUID or its name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Presidio guardrail UUID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization-unique guardrail name. At most 255 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Guardrail description.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"connection_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of a `logfire_gateway_guardrail_connection` in the same organization.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(lowercaseUUIDPattern, "must be a lowercase connection UUID"),
				},
			},
			"on_error": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "What the Gateway does when Presidio fails or times out: `allow` passes the request through, and `block` refuses it.",
				Validators:          []validator.String{stringvalidator.OneOf("allow", "block")},
			},
			"entity_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Presidio entity type to detect, for example `EMAIL_ADDRESS` or `CREDIT_CARD`.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 128)},
			},
			"language": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Language code that Presidio analyzes the text in, for example `en`.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 32)},
			},
			"operator": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "How Presidio anonymizes a detected entity when the action is `redact`. If you omit it, Presidio replaces the entity with its type name.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "`redact` removes the entity. `replace` puts `new_value` in its place.",
						Validators:          []validator.String{stringvalidator.OneOf("redact", "replace")},
					},
					"new_value": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Replacement text. Required when `type` is `replace`, and not allowed otherwise.",
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
					},
				},
			},
			"score_threshold": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Minimum Presidio confidence score, from 0 to 1, for a detection to count.",
				Validators:          []validator.Float64{float64validator.Between(0, 1)},
			},
			"targets": gatewayGuardrailTargetsAttribute(),
			"sample_texts": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Sample texts that the Logfire UI shows in the guardrail editor. They do not change what the Gateway enforces. If you omit this attribute, Terraform keeps the samples stored in Logfire, including samples set in the UI. Set it to `[]` to remove them. At most 20 texts of at most 4000 characters each.",
				Validators: []validator.List{
					listvalidator.SizeAtMost(20),
					listvalidator.ValueStringsAre(stringvalidator.LengthAtMost(4000)),
				},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the Gateway runs the guardrail: `active`, `inactive`, or `error`. It can change outside Terraform, for example when the connection is disabled.",
			},
			"inactive_reason": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Why the guardrail is not active, or null when it is active.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the guardrail was created.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig checks the operator and the target rules that the API enforces.
func (r *GatewayPresidioGuardrailResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	validateGatewayGuardrailTargets(ctx, req.Config, &resp.Diagnostics)
	var object types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("operator"), &object)...)
	if resp.Diagnostics.HasError() || object.IsNull() || object.IsUnknown() {
		return
	}
	var operator GatewayPresidioOperatorModel
	resp.Diagnostics.Append(object.As(ctx, &operator, basetypes.ObjectAsOptions{})...)
	if resp.Diagnostics.HasError() || operator.Type.IsUnknown() || operator.NewValue.IsUnknown() {
		return
	}
	replace := operator.Type.ValueString() == "replace"
	if replace && operator.NewValue.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("operator").AtName("new_value"), "Missing replacement",
			"new_value is required when the operator type is replace.")
	}
	if !replace && !operator.NewValue.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("operator").AtName("new_value"), "Unexpected replacement",
			"new_value can only be set when the operator type is replace.")
	}
}
