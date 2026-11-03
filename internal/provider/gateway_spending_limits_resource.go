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
	_ resource.Resource                = &GatewayProjectSpendingLimitsResource{}
	_ resource.ResourceWithConfigure   = &GatewayProjectSpendingLimitsResource{}
	_ resource.ResourceWithImportState = &GatewayProjectSpendingLimitsResource{}
)

// gatewayOrganizationSpendingLimitsID is the fixed ID of the organization resource,
// because an organization has exactly one set of spending limits.
const gatewayOrganizationSpendingLimitsID = "organization"

// gatewaySpendingLimitMaxUSD is the largest limit that the API accepts.
const gatewaySpendingLimitMaxUSD = 500_000_000

// gatewaySpendingLimitsScopes is the credential requirement that both resources share.
const gatewaySpendingLimitsScopes = "Requires a plan that includes the AI Gateway and a token with " +
	"`organization:read_gateway` and `organization:write_gateway` scopes " +
	"(`organization:read` and `organization:write` are also accepted)."

// GatewaySpendingLimitsWindows holds the limits that both resources share.
type GatewaySpendingLimitsWindows struct {
	DailyUSD   types.Int64 `tfsdk:"daily_usd"`
	WeeklyUSD  types.Int64 `tfsdk:"weekly_usd"`
	MonthlyUSD types.Int64 `tfsdk:"monthly_usd"`
}

// gatewaySpendingLimitsWindowAttributes returns the schema of the three windows.
func gatewaySpendingLimitsWindowAttributes() map[string]schema.Attribute {
	window := func(name, start string) schema.Int64Attribute {
		return schema.Int64Attribute{
			Optional: true,
			MarkdownDescription: "The " + name + " limit in whole US dollars. " + start +
				" Omit it to have no " + name + " limit.",
			Validators: []validator.Int64{int64validator.Between(1, gatewaySpendingLimitMaxUSD)},
		}
	}
	return map[string]schema.Attribute{
		"daily_usd":   window("daily", "A day starts at 00:00 UTC."),
		"weekly_usd":  window("weekly", "A week starts on Monday at 00:00 UTC."),
		"monthly_usd": window("monthly", "A month starts on the 1st at 00:00 UTC."),
	}
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

// configureGatewaySpendingLimitsClient returns the authenticated organization client.
func configureGatewaySpendingLimitsClient(req resource.ConfigureRequest, diags *diag.Diagnostics) *logclient.APIClient {
	if req.ProviderData == nil {
		return nil
	}
	client, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		diags.AddError("Unexpected provider data", "Expected *APIClient.")
		return nil
	}
	return client
}

// NewGatewaySpendingLimitsResource returns the organization Gateway spending limits resource.
func NewGatewaySpendingLimitsResource() resource.Resource { return &GatewaySpendingLimitsResource{} }

// GatewaySpendingLimitsResource manages the Gateway spending limits of the organization.
// The organization always has limits, so create and update set every window, and delete
// removes every limit.
type GatewaySpendingLimitsResource struct {
	client *logclient.APIClient
}

// GatewaySpendingLimitsModel is the organization resource state.
type GatewaySpendingLimitsModel struct {
	ID types.String `tfsdk:"id"`
	GatewaySpendingLimitsWindows
}

// Metadata returns the Terraform resource type.
func (r *GatewaySpendingLimitsResource) Metadata(
	ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_gateway_spending_limits"
}

// Schema defines the limit windows.
func (r *GatewaySpendingLimitsResource) Schema(
	ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse,
) {
	attributes := gatewaySpendingLimitsWindowAttributes()
	attributes["id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "Always `" + gatewayOrganizationSpendingLimitsID + "`.",
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the AI Gateway spending limits of the organization. " +
			"An organization limit caps the Gateway spend of every project and credential in the organization. " +
			gatewaySpendingLimitsScopes,
		Attributes: attributes,
	}
}

// Configure receives the authenticated organization client.
func (r *GatewaySpendingLimitsResource) Configure(
	ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse,
) {
	r.client = configureGatewaySpendingLimitsClient(req, &resp.Diagnostics)
}

// Create sets every window, because the limits already exist before Terraform manages them.
func (r *GatewaySpendingLimitsResource) Create(
	ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse,
) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics, "Create")
}

// Read refreshes every window.
func (r *GatewaySpendingLimitsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
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
}

// Update sets every window.
func (r *GatewaySpendingLimitsResource) Update(
	ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse,
) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics, "Update")
}

// Delete removes every organization limit.
func (r *GatewaySpendingLimitsResource) Delete(
	ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse,
) {
	if _, err := r.client.SetGatewayOrganizationSpendingLimits(ctx, logclient.GatewaySpendingLimits{}); err != nil {
		resp.Diagnostics.AddError("Delete Gateway spending limits failed", err.Error())
	}
}

// ImportState imports the organization limits by the fixed ID.
func (r *GatewaySpendingLimitsResource) ImportState(
	ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse,
) {
	rawID := strings.TrimSpace(req.ID)
	if rawID != gatewayOrganizationSpendingLimitsID {
		resp.Diagnostics.AddError("Invalid import ID",
			`Expected "`+gatewayOrganizationSpendingLimitsID+`". `+
				`Example: terraform import logfire_gateway_spending_limits.this "`+gatewayOrganizationSpendingLimitsID+`"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rawID)...)
}

// apply sends the planned windows and stores the limits that the API returns.
func (r *GatewaySpendingLimitsResource) apply(
	ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics, action string,
) {
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
}

// NewGatewayProjectSpendingLimitsResource returns the project Gateway spending limits resource.
func NewGatewayProjectSpendingLimitsResource() resource.Resource {
	return &GatewayProjectSpendingLimitsResource{}
}

// GatewayProjectSpendingLimitsResource manages the Gateway spending limits of one project.
// The project always has limits, so create and update set every window, and delete
// removes every limit.
type GatewayProjectSpendingLimitsResource struct {
	client *logclient.APIClient
}

// GatewayProjectSpendingLimitsModel is the project resource state.
type GatewayProjectSpendingLimitsModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	GatewaySpendingLimitsWindows
}

// Metadata returns the Terraform resource type.
func (r *GatewayProjectSpendingLimitsResource) Metadata(
	ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_gateway_project_spending_limits"
}

// Schema defines the limit windows and the project they apply to.
func (r *GatewayProjectSpendingLimitsResource) Schema(
	ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse,
) {
	attributes := gatewaySpendingLimitsWindowAttributes()
	attributes["id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The project UUID.",
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attributes["project_id"] = schema.StringAttribute{
		Required:            true,
		MarkdownDescription: "The UUID of the project that the limits apply to. Changing it replaces the resource.",
		Validators: []validator.String{
			stringvalidator.RegexMatches(lowercaseUUIDPattern, "must be a lowercase project UUID"),
		},
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the AI Gateway spending limits of one project. " +
			"When the project goes over a limit, the Gateway blocks every credential in the project " +
			"until the window resets. " + gatewaySpendingLimitsScopes,
		Attributes: attributes,
	}
}

// Configure receives the authenticated organization client.
func (r *GatewayProjectSpendingLimitsResource) Configure(
	ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse,
) {
	r.client = configureGatewaySpendingLimitsClient(req, &resp.Diagnostics)
}

// Create sets every window, because the limits already exist before Terraform manages them.
func (r *GatewayProjectSpendingLimitsResource) Create(
	ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse,
) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics, "Create")
}

// Read refreshes every window.
func (r *GatewayProjectSpendingLimitsResource) Read(
	ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse,
) {
	var state GatewayProjectSpendingLimitsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID := state.ProjectID.ValueString()
	out, err := r.client.GetGatewayProjectSpendingLimits(ctx, projectID)
	if missing, err := r.classify(ctx, projectID, http.MethodGet, err); missing {
		resp.State.RemoveResource(ctx)
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Read Gateway project spending limits failed", err.Error())
		return
	}
	state.ID = types.StringValue(projectID)
	state.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sets every window.
func (r *GatewayProjectSpendingLimitsResource) Update(
	ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse,
) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics, "Update")
}

// Delete removes every limit of the project.
func (r *GatewayProjectSpendingLimitsResource) Delete(
	ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse,
) {
	var state GatewayProjectSpendingLimitsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID := state.ProjectID.ValueString()
	_, err := r.client.SetGatewayProjectSpendingLimits(ctx, projectID, logclient.GatewaySpendingLimits{})
	if missing, err := r.classify(ctx, projectID, http.MethodPatch, err); !missing && err != nil {
		resp.Diagnostics.AddError("Delete Gateway project spending limits failed", err.Error())
	}
}

// ImportState imports the project limits by project UUID.
func (r *GatewayProjectSpendingLimitsResource) ImportState(
	ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse,
) {
	rawID := strings.TrimSpace(req.ID)
	if !lowercaseUUIDPattern.MatchString(rawID) {
		resp.Diagnostics.AddError("Invalid import ID",
			"Expected the lowercase project UUID. "+
				`Example: terraform import logfire_gateway_project_spending_limits.this "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed00"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rawID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), rawID)...)
}

// apply sends the planned windows and stores the limits that the API returns.
func (r *GatewayProjectSpendingLimitsResource) apply(
	ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics, action string,
) {
	var model GatewayProjectSpendingLimitsModel
	diags.Append(plan.Get(ctx, &model)...)
	if diags.HasError() {
		return
	}
	projectID := model.ProjectID.ValueString()
	out, err := r.client.SetGatewayProjectSpendingLimits(ctx, projectID, model.apiLimits())
	if _, err := r.classify(ctx, projectID, http.MethodPatch, err); err != nil {
		diags.AddError(action+" Gateway project spending limits failed", err.Error())
		return
	}
	model.ID = types.StringValue(projectID)
	model.refresh(out)
	diags.Append(state.Set(ctx, &model)...)
}

// classify reports whether a 404 means the project is gone. Otherwise a 404 means
// the instance predates the spending limits API, and the error carries an upgrade
// hint. Supported releases answer a deleted project with 403, which is returned
// unchanged because it cannot be told apart from lost access.
func (r *GatewayProjectSpendingLimitsResource) classify(
	ctx context.Context, projectID, method string, err error,
) (bool, error) {
	if !logclient.IsNotFoundError(err) {
		return false, err
	}
	if projectMissing(ctx, r.client, projectID) {
		return true, err
	}
	return false, logclient.GatewayProjectSpendingLimitsUnavailableError(method, err)
}
