// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

type DashboardResource struct{ configured }

type DashboardModel struct {
	ID         types.String `tfsdk:"id"`
	ProjectID  types.String `tfsdk:"project_id"`
	Name       types.String `tfsdk:"name"`
	Slug       types.String `tfsdk:"slug"`
	Definition types.String `tfsdk:"definition"`
	AllowEdits types.Bool   `tfsdk:"managed_allow_edits"`
}

// dashboardAllowEdits reads managed_allow_edits. Null marks a dashboard from
// before the attribute, which stays editable.
func dashboardAllowEdits(v types.Bool) bool { return v.IsNull() || v.ValueBool() }

// dashboardAllowEditsDefault leaves a new dashboard's value to the create
// response and keeps the stored value of an existing one.
type dashboardAllowEditsDefault struct{}

func (dashboardAllowEditsDefault) Description(context.Context) string {
	return "New dashboards default to false. Existing dashboards keep their value."
}

func (m dashboardAllowEditsDefault) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (dashboardAllowEditsDefault) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.BoolUnknown()
	}
}

// dashboardLock reads managed_allow_edits from a write response. It is null
// when the Logfire version has no managed dashboards or has them turned off.
func dashboardLock(out *logclient.Dashboard) types.Bool {
	if out.ManagedBy == nil || *out.ManagedBy == "" || out.ManagedAllowEdits == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*out.ManagedAllowEdits)
}

// warnIgnoredLock warns when Logfire did not apply a configured
// managed_allow_edits. With managed dashboards turned off, Logfire leaves the
// lock unset, or returns the setting a dashboard that already had a manager
// kept.
func warnIgnoredLock(diags *diag.Diagnostics, planned types.Bool, out *logclient.Dashboard) {
	if !planned.IsNull() && !planned.IsUnknown() && !dashboardLock(out).Equal(planned) {
		diags.AddAttributeWarning(
			path.Root("managed_allow_edits"),
			"Logfire ignored managed_allow_edits",
			"This Logfire version has no managed dashboards, or has them turned off, so the dashboard keeps its current edit setting. Terraform plans the value again on the next run.",
		)
	}
}

func (r *DashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *DashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire dashboard.\n\nThe provider marks the dashboard as managed by the tool that applies it, and Logfire shows a Managed badge on it. A new dashboard is also locked against edits in the Logfire UI and through the Logfire MCP server unless `managed_allow_edits` is `true`. The lock does not cover the dashboards API, which the provider uses. A Logfire instance without managed dashboards ignores the marker. An existing dashboard gets the marker on its next update.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Dashboard UUID assigned by the backend.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project UUID used for API paths.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard display name.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"slug": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard slug used in URLs.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"definition": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard definition JSON payload.",
				PlanModifiers:       []planmodifier.String{dashboardDefinitionPlanModifier{}},
				Validators:          []validator.String{dashboardDefinitionValidator{}},
			},
			"managed_allow_edits": rschema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the Logfire UI and the Logfire MCP server can edit the dashboard. Defaults to `false` for a new dashboard. A Logfire instance without managed dashboards, or with them turned off, ignores a configured value. Terraform then warns and plans the value again on every run until Logfire applies it. A dashboard created by an earlier provider version stays editable until you set the attribute. Removing the attribute keeps the current value. Set it to `true` to allow edits again.",
				PlanModifiers:       []planmodifier.Bool{dashboardAllowEditsDefault{}},
			},
		},
	}
}

func (r *DashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := plan.ProjectID.ValueString()
	projectName, err := r.projectNameForID(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Resolve project name", err.Error())
		return
	}

	defRaw, err := dashboardDefinitionForWrite(
		plan.Definition.ValueString(), plan.Name.ValueString(), projectName)
	if err != nil {
		resp.Diagnostics.AddError("Invalid dashboard definition", err.Error())
		return
	}

	out, err := r.client.CreateDashboard(ctx, projectID, logclient.DashboardCreateRequest{
		Name:             plan.Name.ValueString(),
		Slug:             plan.Slug.ValueString(),
		Definition:       defRaw,
		DashboardManager: r.client.DashboardManager(dashboardAllowEdits(plan.AllowEdits)),
	})
	if err != nil {
		resp.Diagnostics.AddError("Create dashboard failed", err.Error())
		return
	}

	var state DashboardModel
	if err := dashboardReadToModel(out, projectID, &state); err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
	}
	state.Definition = preserveDashboardDefinition(plan.Definition, state.Definition)
	state.AllowEdits = plan.AllowEdits
	if plan.AllowEdits.IsUnknown() {
		state.AllowEdits = dashboardLock(out)
	}
	warnIgnoredLock(&resp.Diagnostics, plan.AllowEdits, out)

	tflog.Trace(ctx, "created dashboard", map[string]any{
		"dashboard_id": state.ID.ValueString(),
		"project_id":   state.ProjectID.ValueString(),
		"slug":         state.Slug.ValueString(),
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	projectID := state.ProjectID.ValueString()
	detail, status, err := r.client.GetDashboard(ctx, projectID, state.ID.ValueString())
	if err != nil {
		if status == 404 || projectMissing(ctx, r.client, projectID) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read dashboard failed", err.Error())
		return
	}

	definition, err := decodeDefinitionObject(detail.Dashboard)
	if err != nil {
		resp.Diagnostics.AddError("Decode dashboard definition", err.Error())
		return
	}
	state.Definition = preserveDashboardDefinition(
		state.Definition, types.StringValue(string(detail.Dashboard)))
	metadata, _ := definition["metadata"].(map[string]any)
	if name, _ := metadata["name"].(string); name != "" {
		state.Name = types.StringValue(name)
	}
	// Only a dashboard with a manager has the lock. Older backends report
	// neither field. Without a manager nothing locks the dashboard, so a
	// configured value shows as a change and the next apply tries to set it
	// again.
	state.AllowEdits = types.BoolNull()
	allowEdits, ok := metadata["managedAllowEdits"].(bool)
	if manager, _ := metadata["managedBy"].(string); ok && manager != "" {
		state.AllowEdits = types.BoolValue(allowEdits)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.ValueString() == "" {
		resp.Diagnostics.AddError(
			"Missing dashboard ID",
			"The dashboard state has no ID. Import the intended dashboard to restore its identity before updating or deleting it.",
		)
		return
	}

	projectID := state.ProjectID.ValueString()
	payload := logclient.DashboardUpdateRequest{
		DashboardManager: r.client.DashboardManager(dashboardAllowEdits(plan.AllowEdits)),
	}
	if !plan.Name.Equal(state.Name) {
		payload.Name = plan.Name.ValueStringPointer()
	}
	if !dashboardDefinitionsEqual(plan.Definition, state.Definition) {
		projectName, err := r.projectNameForID(ctx, projectID)
		if err != nil {
			resp.Diagnostics.AddError("Resolve project name", err.Error())
			return
		}
		definition, err := dashboardDefinitionForWrite(
			plan.Definition.ValueString(), plan.Name.ValueString(), projectName)
		if err != nil {
			resp.Diagnostics.AddError("Invalid dashboard definition", err.Error())
			return
		}
		payload.Definition = &definition
		payload.ReplaceLayout = true
	}

	// Null and true send the same marker, but a dashboard without the marker
	// still needs the write.
	if payload.Name == nil && payload.Definition == nil &&
		plan.AllowEdits.Equal(state.AllowEdits) {
		state.Definition = plan.Definition
		state.Name = plan.Name
		state.AllowEdits = plan.AllowEdits
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	out, err := r.client.UpdateDashboard(ctx, projectID, state.ID.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update dashboard failed", err.Error())
		return
	}

	var newState DashboardModel
	if err := dashboardReadToModel(out, projectID, &newState); err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
	}
	newState.Definition = preserveDashboardDefinition(plan.Definition, newState.Definition)
	newState.AllowEdits = plan.AllowEdits
	warnIgnoredLock(&resp.Diagnostics, plan.AllowEdits, out)

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *DashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.ValueString() == "" {
		resp.Diagnostics.AddError(
			"Missing dashboard ID",
			"The dashboard state has no ID. Import the intended dashboard to restore its identity before updating or deleting it.",
		)
		return
	}

	projectID := state.ProjectID.ValueString()
	err := r.client.DeleteDashboard(ctx, projectID, state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) &&
		!projectMissing(ctx, r.client, projectID) {
		resp.Diagnostics.AddError("Delete dashboard failed", err.Error())
	}
}

func (r *DashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	projectID, summary, ok := importProjectItem(ctx, r.client, req, resp, projectImport[logclient.DashboardSummary]{
		noun:      "dashboard",
		counts:    []int{2, 3},
		missing:   `Expected a non-empty ID. Use either "project_id/dashboard_id/slug" or "project_name/dashboard_slug" (also accepts "," or "|").`,
		invalid:   `Expected "project_id/dashboard_id/slug" or "project_name/dashboard_slug" (also accepts comma or pipe separators).`,
		list:      r.client.ListDashboards,
		keys:      func(d *logclient.DashboardSummary) (string, string) { return d.ID, d.DashboardSlug },
		duplicate: "multiple dashboards with slug %q in project %q; imported the first match",
	})
	if !ok {
		return
	}

	if parts, _ := splitImportParts(req.ID); len(parts) == 3 && parts[2] != summary.DashboardSlug {
		resp.Diagnostics.AddWarning(
			"Import dashboard",
			fmt.Sprintf("provided slug %q does not match dashboard slug %q; using the dashboard found in the API", parts[2], summary.DashboardSlug),
		)
	}

	detail, _, err := r.client.GetDashboard(ctx, projectID, summary.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Import dashboard failed",
			fmt.Sprintf("fetching dashboard %q: %v", summary.ID, err),
		)
		return
	}

	var state DashboardModel
	err = dashboardReadToModel(&logclient.Dashboard{
		ID:            summary.ID,
		ProjectID:     projectID,
		DashboardName: summary.DashboardName,
		DashboardSlug: summary.DashboardSlug,
		Definition:    detail.Dashboard,
	}, projectID, &state)
	if err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DashboardResource) projectNameForID(ctx context.Context, projectID string) (string, error) {
	project, status, err := r.client.GetProject(ctx, projectID)
	if err != nil {
		if status == http.StatusNotFound {
			return "", fmt.Errorf("project %q not found", projectID)
		}
		return "", fmt.Errorf("fetch project %q: %w", projectID, err)
	}
	if project.ProjectName == "" {
		return "", fmt.Errorf("project %q returned empty name", projectID)
	}
	return project.ProjectName, nil
}

func dashboardDefinitionForWrite(raw, name, projectName string) (json.RawMessage, error) {
	payload, err := decodeDefinitionObject([]byte(raw))
	if err != nil {
		return nil, err
	}
	metadata, _ := payload["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["name"] = name
	metadata["project"] = projectName
	payload["metadata"] = metadata
	return json.Marshal(payload)
}

// dashboardReadToModel maps an API dashboard onto m. projectID is the project
// the request targeted, for a response that leaves out its own.
func dashboardReadToModel(d *logclient.Dashboard, projectID string, m *DashboardModel) error {
	if _, err := decodeDefinitionObject(d.Definition); err != nil {
		return err
	}
	m.ID = types.StringValue(d.ID)
	m.ProjectID = types.StringValue(cmp.Or(d.ProjectID, projectID))
	m.Name = types.StringValue(d.DashboardName)
	m.Slug = types.StringValue(d.DashboardSlug)
	m.Definition = types.StringValue(string(d.Definition))
	return nil
}
