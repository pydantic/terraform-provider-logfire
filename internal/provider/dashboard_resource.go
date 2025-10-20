// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var _ resource.Resource = &DashboardResource{}
var _ resource.ResourceWithConfigure = &DashboardResource{}
var _ resource.ResourceWithImportState = &DashboardResource{}

func NewDashboardResource() resource.Resource { return &DashboardResource{} }

type DashboardResource struct {
	client *APIClient
}

type DashboardModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	Project      types.String `tfsdk:"project"`
	Name         types.String `tfsdk:"name"`
	Slug         types.String `tfsdk:"slug"`
	Definition   types.String `tfsdk:"definition"`
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
			"organization": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization slug.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"project": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project slug.",
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
				MarkdownDescription: "Dashboard slug used in URLs and API paths.",
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
			},
		},
	}
}

func (r *DashboardResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*APIClient)
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

	_, defRaw, err := normalizeDefinitionString(plan.Definition.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid dashboard definition", err.Error())
		return
	}

	org := plan.Organization.ValueString()
	prj := plan.Project.ValueString()
	payload := DashboardCreateRequest{
		Name:       plan.Name.ValueString(),
		Slug:       plan.Slug.ValueString(),
		Definition: defRaw,
	}

	out, err := r.client.CreateDashboard(ctx, org, prj, payload)
	if err != nil {
		resp.Diagnostics.AddError("Create dashboard failed", err.Error())
		return
	}

	var state DashboardModel
	state.Organization = plan.Organization
	state.Project = plan.Project
	if err := dashboardReadToModel(out, &state); err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
	}

	tflog.Trace(ctx, "created dashboard", map[string]any{"slug": state.Slug.ValueString(), "org": org, "project": prj})
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

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()
	slug := state.Slug.ValueString()

	detail, status, err := r.client.GetDashboard(ctx, org, prj, slug)
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read dashboard failed", err.Error())
		return
	}

	defStr, err := normalizeDefinitionRaw(detail.Dashboard)
	if err != nil {
		resp.Diagnostics.AddError("Decode dashboard definition", err.Error())
		return
	}

	state.Definition = types.StringValue(defStr)
	if name, ok := dashboardDefinitionName(detail.Dashboard); ok && name != "" {
		state.Name = types.StringValue(name)
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

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()
	slug := state.Slug.ValueString()

	var planDefStr string
	var planDefRaw json.RawMessage
	if !plan.Definition.IsNull() && !plan.Definition.IsUnknown() {
		var err error
		planDefStr, planDefRaw, err = normalizeDefinitionString(plan.Definition.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid dashboard definition", err.Error())
			return
		}
	}

	payload := DashboardUpdateRequest{}
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
			state.Definition = types.StringValue(planDefStr)
		}
		state.Name = plan.Name
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	out, err := r.client.UpdateDashboard(ctx, org, prj, slug, payload)
	if err != nil {
		resp.Diagnostics.AddError("Update dashboard failed", err.Error())
		return
	}

	var newState DashboardModel
	newState.Organization = state.Organization
	newState.Project = state.Project
	if err := dashboardReadToModel(out, &newState); err != nil {
		resp.Diagnostics.AddError("Decode dashboard response", err.Error())
		return
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

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()
	slug := state.Slug.ValueString()

	if err := r.client.DeleteDashboard(ctx, org, prj, slug); err != nil {
		resp.Diagnostics.AddWarning("Delete dashboard", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *DashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use: terraform import logfire_dashboard.prod "organization/project/slug"`,
		)
		return
	}

	parts := strings.Split(req.ID, "/")
	switch len(parts) {
	case 3:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), parts[1])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("slug"), parts[2])...)
	case 1:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("slug"), parts[0])...)
	default:
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "organization/project/slug" or "slug".`,
		)
	}
}

// --- Helpers ---

func normalizeDefinitionString(raw string) (string, json.RawMessage, error) {
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", nil, fmt.Errorf("invalid JSON: %w", err)
	}
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
	if metaVal, ok := payload["metadata"]; ok {
		if meta, ok := metaVal.(map[string]any); ok {
			delete(meta, "createdAt")
			delete(meta, "updatedAt")
			delete(meta, "version")
			payload["metadata"] = meta
		}
	}
	normalized, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("normalize JSON: %w", err)
	}
	return string(normalized), nil
}

func dashboardDefinitionName(raw json.RawMessage) (string, bool) {
	var payload struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", false
	}
	return payload.Metadata.Name, payload.Metadata.Name != ""
}

func dashboardReadToModel(d *Dashboard, m *DashboardModel) error {
	defStr, err := normalizeDefinitionRaw(d.Definition)
	if err != nil {
		return err
	}
	m.ID = types.StringValue(d.ID)
	m.Name = types.StringValue(d.DashboardName)
	m.Slug = types.StringValue(d.DashboardSlug)
	m.Definition = types.StringValue(defStr)
	return nil
}
