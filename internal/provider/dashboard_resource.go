// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ resource.Resource = &DashboardResource{}
var _ resource.ResourceWithConfigure = &DashboardResource{}
var _ resource.ResourceWithImportState = &DashboardResource{}

func NewDashboardResource() resource.Resource { return &DashboardResource{} }

type DashboardResource struct {
	client *logclient.APIClient
}

type DashboardModel struct {
	ID         types.String          `tfsdk:"id"`
	ProjectID  types.String          `tfsdk:"project_id"`
	Name       types.String          `tfsdk:"name"`
	Slug       types.String          `tfsdk:"slug"`
	Definition definitionStringValue `tfsdk:"definition"`
}

func (r *DashboardResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *DashboardResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire dashboard.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Dashboard UUID assigned by the backend.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project UUID used for API paths.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard display name.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"slug": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard slug used in URLs.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"definition": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard definition JSON payload.",
				CustomType:          definitionStringType{},
			},
		},
	}
}

func (r *DashboardResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *APIClient, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *DashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan DashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ProjectID.IsNull() || plan.ProjectID.IsUnknown() || plan.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "The dashboard requires a project_id to construct API paths.")
		return
	}

	projectID := plan.ProjectID.ValueString()
	projectName, err := r.projectNameForID(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Resolve project name", err.Error())
		return
	}

	_, defRaw, err := normalizeDefinitionString(plan.Definition.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid dashboard definition", err.Error())
		return
	}

	var defPayload map[string]any
	if err := json.Unmarshal(defRaw, &defPayload); err != nil {
		resp.Diagnostics.AddError("Failed to unmarshal definition", err.Error())
		return
	}
	meta, ok := defPayload["metadata"].(map[string]any)
	if !ok || meta == nil {
		meta = map[string]any{}
	}
	meta["name"] = plan.Name.ValueString()
	meta["project"] = projectName
	defPayload["metadata"] = meta

	finalDefRaw, err := json.Marshal(defPayload)
	if err != nil {
		resp.Diagnostics.AddError("Failed to marshal definition", err.Error())
		return
	}

	payload := logclient.DashboardCreateRequest{
		Name:       plan.Name.ValueString(),
		Slug:       plan.Slug.ValueString(),
		Definition: finalDefRaw,
	}

	out, err := r.client.CreateDashboard(ctx, projectID, payload)
	if err != nil {
		resp.Diagnostics.AddError("Create dashboard failed", err.Error())
		return
	}

	var state DashboardModel
	if err := dashboardReadToModel(out, &state); err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = types.StringValue(projectID)
	}

	tflog.Trace(ctx, "created dashboard", map[string]any{
		"dashboard_id": state.ID.ValueString(),
		"project_id":   state.ProjectID.ValueString(),
		"slug":         state.Slug.ValueString(),
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state DashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot read dashboard because the state is missing an ID.")
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot read dashboard because the state is missing a project_id.")
		return
	}

	projectID := state.ProjectID.ValueString()

	detail, status, err := r.client.GetDashboard(ctx, projectID, state.ID.ValueString())
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read dashboard failed", err.Error())
		return
	}

	metaName := ""
	var raw map[string]any
	if err := json.Unmarshal(detail.Dashboard, &raw); err == nil {
		if meta, ok := raw["metadata"].(map[string]any); ok {
			if n, ok := meta["name"].(string); ok && n != "" {
				metaName = n
			}
		}
	}

	defStr, err := normalizeDefinitionRaw(detail.Dashboard)
	if err != nil {
		resp.Diagnostics.AddError("Decode dashboard definition", err.Error())
		return
	}

	state.Definition = newDefinitionStringValue(defStr)
	state.ProjectID = types.StringValue(projectID)
	if metaName != "" {
		state.Name = types.StringValue(metaName)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan DashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state DashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot update dashboard because the current state has no ID.")
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot update dashboard because the current state has no project_id.")
		return
	}

	projectID := state.ProjectID.ValueString()
	projectName, err := r.projectNameForID(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Resolve project name", err.Error())
		return
	}
	dashboardID := state.ID.ValueString()

	var planDefStr string
	var planDefRaw json.RawMessage
	if !plan.Definition.IsNull() && !plan.Definition.IsUnknown() {
		var err error
		planDefStr, planDefRaw, err = normalizeDefinitionString(plan.Definition.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid dashboard definition", err.Error())
			return
		}

		var defPayload map[string]any
		if err := json.Unmarshal(planDefRaw, &defPayload); err != nil {
			resp.Diagnostics.AddError("Failed to unmarshal definition", err.Error())
			return
		}
		meta, ok := defPayload["metadata"].(map[string]any)
		if !ok || meta == nil {
			meta = map[string]any{}
		}
		meta["name"] = plan.Name.ValueString()
		meta["project"] = projectName
		defPayload["metadata"] = meta
		planDefRaw, err = json.Marshal(defPayload)
		if err != nil {
			resp.Diagnostics.AddError("Failed to marshal definition", err.Error())
			return
		}
	}

	payload := logclient.DashboardUpdateRequest{}
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		newName := plan.Name.ValueString()
		if state.Name.IsNull() || state.Name.IsUnknown() || newName != state.Name.ValueString() {
			payload.Name = &newName
		}
	}
	if len(planDefRaw) > 0 {
		current := state.Definition.ValueString()
		if state.Definition.IsNull() || state.Definition.IsUnknown() || planDefStr != current {
			defCopy := planDefRaw
			payload.Definition = &defCopy
		}
	}

	if payload.Name == nil && payload.Definition == nil {
		// No remote changes needed; ensure state reflects canonical definition if provided.
		if planDefStr != "" {
			state.Definition = newDefinitionStringValue(planDefStr)
		}
		state.Name = plan.Name
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	out, err := r.client.UpdateDashboard(ctx, projectID, dashboardID, payload)
	if err != nil {
		resp.Diagnostics.AddError("Update dashboard failed", err.Error())
		return
	}

	var newState DashboardModel
	newState.ProjectID = types.StringValue(projectID)
	if err := dashboardReadToModel(out, &newState); err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
	}
	if newState.ProjectID.IsNull() || newState.ProjectID.IsUnknown() || newState.ProjectID.ValueString() == "" {
		newState.ProjectID = types.StringValue(projectID)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *DashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state DashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot delete dashboard because the current state has no ID.")
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot delete dashboard because the current state has no project_id.")
		return
	}

	projectID := state.ProjectID.ValueString()
	dashboardID := state.ID.ValueString()

	if err := r.client.DeleteDashboard(ctx, projectID, dashboardID); err != nil {
		if logclient.IsRateLimitError(err) {
			resp.Diagnostics.AddError("Delete dashboard", fmt.Sprintf("rate limited while deleting dashboard: %v", err))
			return
		}
		resp.Diagnostics.AddWarning("Delete dashboard", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *DashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	rawID := strings.TrimSpace(req.ID)
	if rawID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use either "project_id/dashboard_id/slug" or "project_name/dashboard_slug" (also accepts "," or "|").`,
		)
		return
	}

	parts, err := splitImportParts(rawID, 2, 3)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "project_id/dashboard_id/slug" or "project_name/dashboard_slug" (also accepts comma or pipe separators).`,
		)
		return
	}

	projectKey := parts[0]
	dashboardKey := parts[1]

	var providedSlug string
	if len(parts) == 3 {
		providedSlug = parts[2]
	}

	projectID, projectName, err := findProjectByNameOrID(ctx, r.client, projectKey)
	if err != nil {
		resp.Diagnostics.AddError("Import dashboard failed", err.Error())
		return
	}

	dashboards, err := r.client.ListDashboards(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Import dashboard failed", fmt.Sprintf("listing dashboards for project %q: %v", projectID, err))
		return
	}

	var (
		summary     *logclient.DashboardSummary
		slugMatches []*logclient.DashboardSummary
	)
	for i := range dashboards {
		d := &dashboards[i]
		if d.ID == dashboardKey {
			summary = d
			break
		}
		// If a slug was provided, prefer it; otherwise fall back to matching the key as slug.
		slugCandidate := dashboardKey
		if providedSlug != "" {
			slugCandidate = providedSlug
		}
		if d.DashboardSlug == slugCandidate {
			slugMatches = append(slugMatches, d)
		}
	}

	if summary == nil {
		if len(slugMatches) == 0 {
			resp.Diagnostics.AddError("Import dashboard failed", fmt.Sprintf("dashboard %q not found in project %q", dashboardKey, projectName))
			return
		}
		summary = slugMatches[0]
		if len(slugMatches) > 1 {
			resp.Diagnostics.AddWarning("Import dashboard", fmt.Sprintf("multiple dashboards with slug %q in project %q; imported the first match", slugMatches[0].DashboardSlug, projectName))
		}
	}

	if providedSlug != "" && providedSlug != summary.DashboardSlug {
		resp.Diagnostics.AddWarning("Import dashboard", fmt.Sprintf("provided slug %q does not match dashboard slug %q; using the dashboard found in the API", providedSlug, summary.DashboardSlug))
	}

	detail, _, err := r.client.GetDashboard(ctx, projectID, summary.ID)
	if err != nil {
		resp.Diagnostics.AddError("Import dashboard failed", fmt.Sprintf("fetching dashboard %q: %v", summary.ID, err))
		return
	}

	dashboard := &logclient.Dashboard{
		ID:            summary.ID,
		ProjectID:     projectID,
		DashboardName: summary.DashboardName,
		DashboardSlug: summary.DashboardSlug,
		Definition:    detail.Dashboard,
	}

	var state DashboardModel
	if err := dashboardReadToModel(dashboard, &state); err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = types.StringValue(projectID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// --- Helpers ---

func (r *DashboardResource) projectNameForID(ctx context.Context, projectID string) (string, error) {
	if r.client == nil {
		return "", fmt.Errorf("provider is not configured")
	}
	if projectID == "" {
		return "", fmt.Errorf("project_id is empty")
	}

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

func normalizeDefinitionString(raw string) (string, json.RawMessage, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", nil, fmt.Errorf("invalid JSON: %w", err)
	}
	scrubDefinitionMetadata(payload)
	normalized, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("normalize JSON: %w", err)
	}
	return string(normalized), json.RawMessage(normalized), nil
}

func normalizeDefinitionRaw(raw json.RawMessage) (string, error) {
	if raw == nil {
		return "", fmt.Errorf("definition is empty")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("invalid definition JSON: %w", err)
	}
	scrubDefinitionMetadata(payload)
	normalized, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("normalize JSON: %w", err)
	}
	return string(normalized), nil
}

func scrubDefinitionMetadata(payload map[string]any) {
	if metaVal, ok := payload["metadata"]; ok {
		if meta, ok := metaVal.(map[string]any); ok {
			delete(meta, "name")
			delete(meta, "project")
			delete(meta, "createdAt")
			delete(meta, "updatedAt")
			delete(meta, "version")
			payload["metadata"] = meta
		}
	}
}

func dashboardReadToModel(d *logclient.Dashboard, m *DashboardModel) error {
	defStr, err := normalizeDefinitionRaw(d.Definition)
	if err != nil {
		return err
	}
	m.ID = types.StringValue(d.ID)
	if d.ProjectID == "" {
		m.ProjectID = types.StringNull()
	} else {
		m.ProjectID = types.StringValue(d.ProjectID)
	}
	m.Name = types.StringValue(d.DashboardName)
	m.Slug = types.StringValue(d.DashboardSlug)
	m.Definition = newDefinitionStringValue(defStr)
	return nil
}
