// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"math"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	gatewayOptimizationCategories   = []string{"prompt_hygiene", "tool_use", "verbosity", "quality", "style"}
	gatewayOptimizationMessageRoles = []string{"system", "user"}
	gatewayOptimizationFailureModes = []string{"fail_open", "fail_closed"}
	gatewayOptimizationActions      = []string{"observe", "transform"}
)

var gatewayOptimizationTargetType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"endpoint_slug":         types.StringType,
	"model_pattern":         types.StringType,
	"agent_name":            types.StringType,
	"system_prompt_pattern": types.StringType,
}}

// Schema defines the public Gateway optimization rule configuration.
func (r *GatewayOptimizationRuleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an AI Gateway optimization rule and the endpoints it applies to.\n\n" +
			"~> **Beta:** this resource is backed by a Logfire API in Beta. " +
			"Its schema and behavior may change in future releases.\n\n" +
			"Requires Growth, Enterprise Cloud, or self-hosted and a token with " +
			"organization:read_gateway and organization:write_gateway scopes " +
			"(organization:read and organization:write are also accepted). " +
			"The resource manages user rules and rules installed from the Logfire catalog. " +
			"On a catalog rule (`source` is `builtin`), only `enabled` and `targets` can change. " +
			"Keep every other argument equal to the value that Logfire stores. " +
			"Import accepts the rule UUID or its organization-unique name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Rule UUID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization-unique rule name, at most 2,048 bytes in UTF-8.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 2048)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Rule description.",
			},
			"category": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Catalog category of the rule. One of `prompt_hygiene`, `tool_use`, `verbosity`, `quality`, or `style`.",
				Validators:          []validator.String{stringvalidator.OneOf(gatewayOptimizationCategories...)},
			},
			"instruction": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Text that a `transform` rule appends to the message that `message_role` selects.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"message_role": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("system"),
				MarkdownDescription: "Message that the instruction is appended to. `system` appends to the system prompt " +
					"and creates one if the request has none. `user` appends to the final user turn, and the Gateway " +
					"skips the rule for a request without one. Defaults to `system`.",
				Validators: []validator.String{stringvalidator.OneOf(gatewayOptimizationMessageRoles...)},
			},
			"trigger_pattern": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Regular expression that the request must match for the rule to apply. " +
					"Omit it to apply the rule to every matching request. Logfire checks that the Gateway can run the pattern.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"priority": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(100),
				MarkdownDescription: "When targets are equally specific, the Gateway applies rules with a higher priority first. " +
					"Defaults to 100.",
				Validators: []validator.Int64{int64validator.Between(math.MinInt32, math.MaxInt32)},
			},
			"failure_mode": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("fail_open"),
				MarkdownDescription: "What the Gateway does when it cannot evaluate the rule. `fail_open` skips the rule " +
					"and serves the request. `fail_closed` refuses the request. Defaults to `fail_open`.",
				Validators: []validator.String{stringvalidator.OneOf(gatewayOptimizationFailureModes...)},
			},
			"action": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("transform"),
				MarkdownDescription: "`transform` appends the instruction to the request. `observe` records that the rule " +
					"matched and leaves the request unchanged. Defaults to `transform`.",
				Validators: []validator.String{stringvalidator.OneOf(gatewayOptimizationActions...)},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the Gateway applies the rule. Defaults to `true`.",
			},
			"targets": schema.SetNestedAttribute{
				Optional: true,
				Computed: true,
				Default:  setdefault.StaticValue(types.SetValueMust(gatewayOptimizationTargetType, []attr.Value{})),
				MarkdownDescription: "Complete set of request selectors that the rule applies to, at most 100. " +
					"A change replaces the whole set. A rule without targets applies to no request. Defaults to an empty set. " +
					"If an endpoint is deleted, Logfire deletes its targets, and the next plan shows the change.",
				Validators: []validator.Set{setvalidator.SizeAtMost(100)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"endpoint_slug": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Slug of a Gateway endpoint in the same organization, for example " +
								"`logfire_gateway_endpoint.default.slug`.",
							Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"model_pattern": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Regular expression that the requested model must match. Omit it to match every model.",
							Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"agent_name": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Agent name that the request must carry. Omit it to match every agent.",
							Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"system_prompt_pattern": schema.StringAttribute{
							Optional: true,
							MarkdownDescription: "Regular expression that the system prompt must match. " +
								"Omit it to match every system prompt.",
							Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
						},
					},
				},
			},
			"source": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "`user` for a rule created through Terraform, the API, or the editor. " +
					"`builtin` for a rule installed from the Logfire catalog.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"template_slug": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Catalog template of a `builtin` rule. Null for a `user` rule.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the rule was created.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}
