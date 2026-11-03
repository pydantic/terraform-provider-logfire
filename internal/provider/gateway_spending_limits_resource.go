// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var (
	_ resource.Resource                = &GatewaySpendingLimitsResource{}
	_ resource.ResourceWithConfigure   = &GatewaySpendingLimitsResource{}
	_ resource.ResourceWithImportState = &GatewaySpendingLimitsResource{}
)

// gatewayOrganizationSpendingLimitsID is the fixed ID of the organization resource,
// because an organization has exactly one set of spending limits.
const gatewayOrganizationSpendingLimitsID = "organization"

// gatewaySpendingLimitMaxUSD is the largest limit that the API accepts.
const gatewaySpendingLimitMaxUSD = 500_000_000

// NewGatewaySpendingLimitsResource returns the organization Gateway spending limits resource.
func NewGatewaySpendingLimitsResource() resource.Resource { return &GatewaySpendingLimitsResource{} }

// NewGatewayProjectSpendingLimitsResource returns the project Gateway spending limits resource.
func NewGatewayProjectSpendingLimitsResource() resource.Resource {
	return &GatewaySpendingLimitsResource{project: true}
}

// GatewaySpendingLimitsResource manages the Gateway spending limits of the organization,
// or of one project when project is true. Each scope always has limits, so create and
// update set every window, and delete removes every limit.
type GatewaySpendingLimitsResource struct {
	client  *logclient.APIClient
	project bool
}

// GatewaySpendingLimitsWindows holds the limits that both resources share.
type GatewaySpendingLimitsWindows struct {
	DailyUSD   types.Int64 `tfsdk:"daily_usd"`
	WeeklyUSD  types.Int64 `tfsdk:"weekly_usd"`
	MonthlyUSD types.Int64 `tfsdk:"monthly_usd"`
}

// GatewaySpendingLimitsModel is the organization resource state.
type GatewaySpendingLimitsModel struct {
	ID types.String `tfsdk:"id"`
	GatewaySpendingLimitsWindows
}

// GatewayProjectSpendingLimitsModel is the project resource state.
type GatewayProjectSpendingLimitsModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	GatewaySpendingLimitsWindows
}

// Metadata returns the Terraform resource type.
func (r *GatewaySpendingLimitsResource) Metadata(
	ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse,
) {
	if r.project {
		resp.TypeName = req.ProviderTypeName + "_gateway_project_spending_limits"
		return
	}
	resp.TypeName = req.ProviderTypeName + "_gateway_spending_limits"
}

// Schema defines the limit windows and, for a project, the project it applies to.
func (r *GatewaySpendingLimitsResource) Schema(
	ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse,
) {
	window := func(name, start string) schema.Int64Attribute {
		return schema.Int64Attribute{
			Optional: true,
			MarkdownDescription: "The " + name + " limit in whole US dollars. " + start +
				" Omit it to have no " + name + " limit.",
			Validators: []validator.Int64{int64validator.Between(1, gatewaySpendingLimitMaxUSD)},
		}
	}
	attributes := map[string]schema.Attribute{
		"daily_usd":   window("daily", "A day starts at 00:00 UTC."),
		"weekly_usd":  window("weekly", "A week starts on Monday at 00:00 UTC."),
		"monthly_usd": window("monthly", "A month starts on the 1st at 00:00 UTC."),
	}
	scopes := "Requires Growth, Enterprise Cloud, or self-hosted and a token with " +
		"organization:read_gateway and organization:write_gateway scopes " +
		"(organization:read and organization:write are also accepted). "
	if r.project {
		attributes["id"] = schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The project UUID.",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
		attributes["project_id"] = schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "The UUID of the project that the limits apply to. Changing it replaces the resource.",
			Validators: []validator.String{
				stringvalidator.RegexMatches(uuidPattern, "must be a project UUID"),
			},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		}
		resp.Schema = schema.Schema{
			MarkdownDescription: "Manages the AI Gateway spending limits of one project. " +
				"When the project goes over a limit, the Gateway blocks every credential in the project " +
				"until the window resets. " + scopes +
				"The resource manages all three windows. Destroying it removes every limit of the project. " +
				"Import accepts the project UUID.",
			Attributes: attributes,
		}
		return
	}
	attributes["id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "Always `" + gatewayOrganizationSpendingLimitsID + "`.",
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the AI Gateway spending limits of the organization. " +
			"An organization limit caps the Gateway spend of every project and credential in the organization. " +
			scopes +
			"Declare at most one of this resource for each organization. " +
			"The resource manages all three windows. Destroying it removes every organization limit. " +
			"Import accepts `" + gatewayOrganizationSpendingLimitsID + "`.",
		Attributes: attributes,
	}
}

// Configure receives the authenticated organization client.
func (r *GatewaySpendingLimitsResource) Configure(
	ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Expected *APIClient.")
		return
	}
	r.client = client
}

// Create sets every window, because the limits already exist before Terraform manages them.
func (r *GatewaySpendingLimitsResource) Create(
	ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse,
) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics, "Create")
}

// Read refreshes every window.
func (r *GatewaySpendingLimitsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.project {
		var state GatewaySpendingLimitsModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		out, err := r.client.GetGatewayOrganizationSpendingLimits(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Read Gateway spending limits failed", err.Error())
			return
		}
		state.ID = types.StringValue(gatewayOrganizationSpendingLimitsID)
		state.refresh(out)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	var state GatewayProjectSpendingLimitsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID := state.ProjectID.ValueString()
	out, err := r.client.GetGatewayProjectSpendingLimits(ctx, projectID)
	if logclient.IsNotFoundError(err) && projectMissing(ctx, r.client, projectID) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway project spending limits failed",
			logclient.GatewayProjectSpendingLimitsUnavailableError(http.MethodGet, err).Error())
		return
	}
	state.ID = types.StringValue(projectID)
	state.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sets every window.
func (r *GatewaySpendingLimitsResource) Update(
	ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse,
) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics, "Update")
}

// Delete removes every limit of the scope.
func (r *GatewaySpendingLimitsResource) Delete(
	ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse,
) {
	if !r.project {
		if _, err := r.client.SetGatewayOrganizationSpendingLimits(ctx, logclient.GatewaySpendingLimits{}); err != nil {
			resp.Diagnostics.AddError("Delete Gateway spending limits failed", err.Error())
		}
		return
	}
	var state GatewayProjectSpendingLimitsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID := state.ProjectID.ValueString()
	_, err := r.client.SetGatewayProjectSpendingLimits(ctx, projectID, logclient.GatewaySpendingLimits{})
	if logclient.IsNotFoundError(err) && projectMissing(ctx, r.client, projectID) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Delete Gateway project spending limits failed",
			logclient.GatewayProjectSpendingLimitsUnavailableError(http.MethodPatch, err).Error())
	}
}

// ImportState imports the organization limits by the fixed ID, or project limits by project UUID.
func (r *GatewaySpendingLimitsResource) ImportState(
	ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse,
) {
	rawID := strings.TrimSpace(req.ID)
	if !r.project {
		if rawID != gatewayOrganizationSpendingLimitsID {
			resp.Diagnostics.AddError("Invalid import ID",
				`Expected "`+gatewayOrganizationSpendingLimitsID+`". `+
					`Example: terraform import logfire_gateway_spending_limits.this "`+gatewayOrganizationSpendingLimitsID+`"`)
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rawID)...)
		return
	}
	if !uuidPattern.MatchString(rawID) {
		resp.Diagnostics.AddError("Invalid import ID",
			"Expected the project UUID. "+
				`Example: terraform import logfire_gateway_project_spending_limits.this "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed00"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rawID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), rawID)...)
}

// apply sends the planned windows and stores the limits that the API returns.
func (r *GatewaySpendingLimitsResource) apply(
	ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics, action string,
) {
	if !r.project {
		var model GatewaySpendingLimitsModel
		diags.Append(plan.Get(ctx, &model)...)
		if diags.HasError() {
			return
		}
		out, err := r.client.SetGatewayOrganizationSpendingLimits(ctx, model.apiLimits())
		if err != nil {
			diags.AddError(action+" Gateway spending limits failed", err.Error())
			return
		}
		model.ID = types.StringValue(gatewayOrganizationSpendingLimitsID)
		model.refresh(out)
		diags.Append(state.Set(ctx, &model)...)
		return
	}
	var model GatewayProjectSpendingLimitsModel
	diags.Append(plan.Get(ctx, &model)...)
	if diags.HasError() {
		return
	}
	projectID := model.ProjectID.ValueString()
	out, err := r.client.SetGatewayProjectSpendingLimits(ctx, projectID, model.apiLimits())
	if logclient.IsNotFoundError(err) && !projectMissing(ctx, r.client, projectID) {
		err = logclient.GatewayProjectSpendingLimitsUnavailableError(http.MethodPatch, err)
	}
	if err != nil {
		diags.AddError(action+" Gateway project spending limits failed", err.Error())
		return
	}
	model.ID = types.StringValue(projectID)
	model.refresh(out)
	diags.Append(state.Set(ctx, &model)...)
}

func (m *GatewaySpendingLimitsWindows) apiLimits() logclient.GatewaySpendingLimits {
	return logclient.GatewaySpendingLimits{
		DailyUSD:   m.DailyUSD.ValueInt64Pointer(),
		WeeklyUSD:  m.WeeklyUSD.ValueInt64Pointer(),
		MonthlyUSD: m.MonthlyUSD.ValueInt64Pointer(),
	}
}

func (m *GatewaySpendingLimitsWindows) refresh(out *logclient.GatewaySpendingLimits) {
	m.DailyUSD = types.Int64PointerValue(out.DailyUSD)
	m.WeeklyUSD = types.Int64PointerValue(out.WeeklyUSD)
	m.MonthlyUSD = types.Int64PointerValue(out.MonthlyUSD)
}
