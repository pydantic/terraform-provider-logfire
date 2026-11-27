// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// gatewayGuardrailBaseURLPattern accepts the service root form that Logfire stores:
// no credentials, query, fragment, or trailing slash.
var gatewayGuardrailBaseURLPattern = regexp.MustCompile(`^https?://[^\s/?#@]+(?:/[^\s?#]*[^/\s?#])?$`)

// Schema defines the supported guardrail connection configuration.
func (r *GatewayGuardrailConnectionResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an AI Gateway connection to a Presidio service that `logfire_gateway_presidio_guardrail` resources use.\n\n" + gatewayGuardrailBetaNote + "\n\n" +
			gatewayGuardrailScopesNote + " Creating or changing a connection also needs a plan with Presidio guardrails (Enterprise Cloud or self-hosted). Logfire tests an enabled connection before it saves it, so an apply fails while Presidio is unreachable. Import accepts the connection UUID or its organization-unique name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Connection UUID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization-unique connection name. At most 255 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"base_url": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Root URL of the Presidio service, for example `https://presidio.example.com`. Logfire Cloud accepts only public HTTPS addresses. A self-hosted instance also accepts HTTP and private addresses. A change resends `bearer_token`, because Logfire never sends a stored token to a new address.",
				Validators: []validator.String{
					stringvalidator.LengthAtMost(2048),
					stringvalidator.RegexMatches(gatewayGuardrailBaseURLPattern,
						"must be an http or https URL without credentials, a query, or a trailing slash"),
				},
			},
			"auth_type": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("none"),
				MarkdownDescription: "Authentication that Logfire sends to Presidio: `none` or `bearer`. Defaults to `none`.",
				Validators:          []validator.String{stringvalidator.OneOf("none", "bearer")},
			},
			"bearer_token": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Bearer token for Presidio. Required when `auth_type` is `bearer`, and not allowed otherwise. Changing it rotates the token in place. Logfire never returns this value; Terraform retains it in state. External token changes cannot be detected. After import, the next apply sets the configured token.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"timeout_ms": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(2000),
				MarkdownDescription: "Timeout of one Presidio request, in milliseconds, from 100 to 10000. Defaults to 2000.",
				Validators:          []validator.Int64{int64validator.Between(100, 10000)},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the Gateway uses the connection. Defaults to true.",
			},
			"guardrail_count": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Number of Presidio guardrails that use the connection.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the connection was created.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig checks that bearer_token is set exactly when auth_type is bearer.
func (r *GatewayGuardrailConnectionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config GatewayGuardrailConnectionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.AuthType.IsUnknown() || config.BearerToken.IsUnknown() {
		return
	}
	bearer := config.AuthType.ValueString() == "bearer"
	if bearer && config.BearerToken.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("bearer_token"), "Missing bearer token",
			"bearer_token is required when auth_type is bearer.")
	}
	if !bearer && !config.BearerToken.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("bearer_token"), "Unexpected bearer token",
			"bearer_token can only be set when auth_type is bearer.")
	}
}
