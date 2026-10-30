// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"math"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var lowercaseUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Schema defines the supported public Gateway endpoint configuration.
func (r *GatewayEndpointResource) Schema(
	ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an organization-owned AI Gateway endpoint and the providers it routes to. " +
			"Requires Growth, Enterprise Cloud, or self-hosted and a token with " +
			"organization:read_gateway and organization:write_gateway scopes " +
			"(organization:read and organization:write are also accepted). " +
			"Endpoints that use built-in providers or more than 100 providers are not supported. " +
			"Import accepts the endpoint UUID or its organization-unique slug.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Endpoint UUID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"slug": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization-unique endpoint slug. Changing it replaces the endpoint.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 64),
					stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`),
						"must start with a letter or digit and contain only letters, digits, dots, underscores, or hyphens"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Endpoint description.",
				Validators:          []validator.String{stringvalidator.LengthAtMost(1000)},
			},
			"providers": schema.SetNestedAttribute{
				Required: true,
				MarkdownDescription: "Complete set of providers the endpoint routes to. " +
					"A change replaces the whole set in one request. At least one provider must be active.",
				Validators: []validator.Set{setvalidator.SizeBetween(1, 100)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"provider_id": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "UUID of a `logfire_gateway_provider` in the same organization.",
							Validators: []validator.String{
								stringvalidator.RegexMatches(lowercaseUUIDPattern, "must be a lowercase provider UUID"),
							},
						},
						"priority": schema.Int64Attribute{
							Optional: true,
							MarkdownDescription: "The Gateway tries providers with a higher priority first. " +
								"Providers with the same priority are tried in a random order that follows their weights. " +
								"Defaults to 0.",
							Validators: []validator.Int64{int64validator.Between(0, math.MaxInt32)},
						},
						"weight": schema.Float64Attribute{
							Optional:            true,
							MarkdownDescription: "Positive relative weight among providers with the same priority. Defaults to 1.",
						},
						"active": schema.BoolAttribute{
							Optional:            true,
							MarkdownDescription: "Whether the Gateway routes to this provider. Defaults to true.",
						},
					},
				},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the endpoint was created.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig checks the description and the provider rules the API enforces on the whole set.
func (r *GatewayEndpointResource) ValidateConfig(
	ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse,
) {
	var description types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("description"), &description)...)
	if v := description.ValueString(); strings.TrimSpace(v) != v {
		resp.Diagnostics.AddAttributeError(path.Root("description"), "Invalid description",
			"description must not start or end with whitespace, because Logfire trims it.")
	}
	var set types.Set
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("providers"), &set)...)
	if resp.Diagnostics.HasError() || set.IsNull() || set.IsUnknown() {
		return
	}
	for _, element := range set.Elements() {
		if element.IsUnknown() {
			return
		}
	}
	var providers []GatewayEndpointProviderModel
	resp.Diagnostics.Append(set.ElementsAs(ctx, &providers, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	seen := map[string]bool{}
	anyActive, activeUnknown := false, false
	for _, p := range providers {
		if !p.ProviderID.IsUnknown() && !p.ProviderID.IsNull() {
			id := p.ProviderID.ValueString()
			if seen[id] {
				resp.Diagnostics.AddAttributeError(path.Root("providers"), "Duplicate provider",
					"Provider "+id+" appears more than once.")
			}
			seen[id] = true
		}
		if !p.Weight.IsUnknown() && !p.Weight.IsNull() {
			if w := p.Weight.ValueFloat64(); !(w > 0) || math.IsInf(w, 0) || float32(w) == 0 || math.IsInf(float64(float32(w)), 0) {
				resp.Diagnostics.AddAttributeError(path.Root("providers"), "Invalid weight",
					"weight must be a positive finite number that fits in a 32-bit float.")
			}
		}
		activeUnknown = activeUnknown || p.Active.IsUnknown()
		anyActive = anyActive || p.Active.IsNull() || p.Active.ValueBool()
	}
	if !anyActive && !activeUnknown {
		resp.Diagnostics.AddAttributeError(path.Root("providers"), "No active provider",
			"At least one provider must be active.")
	}
}
