// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var _ resource.Resource = &ProjectResource{}
var _ resource.ResourceWithImportState = &ProjectResource{}

func NewProjectResource() resource.Resource { return &ProjectResource{} }

type ProjectResource struct {
	client *APIClient
}

type ProjectModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	ProjectName  types.String `tfsdk:"project_name"`
	Description  types.String `tfsdk:"description"`
}

func (r *ProjectResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *ProjectResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire project.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Opaque backend project ID (not the slug).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project name/slug. Must be unique within the organization.",
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Project description.",
			},
		},
	}
}

func (r *ProjectResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// --- helpers ---

func projectReadToModel(p *ProjectRead, m *ProjectModel) {
	m.ID = types.StringValue(p.ID)
	m.ProjectName = types.StringValue(p.ProjectName)
	m.Description = types.StringValue(p.Description)
}

func projectModelToCreate(m *ProjectModel) ProjectCreate {
	return ProjectCreate{
		ProjectName: m.ProjectName.ValueString(),
		Description: m.Description.ValueString(),
	}
}

// --- CRUD ---

func (r *ProjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan ProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := plan.Organization.ValueString()
	in := projectModelToCreate(&plan)

	out, err := r.client.CreateProject(ctx, org, in)
	if err != nil {
		resp.Diagnostics.AddError("Create project failed", err.Error())
		return
	}

	var state ProjectModel
	// preserve org from plan
	state.Organization = plan.Organization
	projectReadToModel(out, &state)

	tflog.Trace(ctx, "created project", map[string]any{"id": state.ID.ValueString(), "org": org})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ProjectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()

	out, status, err := r.client.GetProject(ctx, org, state.ProjectName.ValueString())
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read project failed", err.Error())
		return
	}

	orgVal := state.Organization
	projectReadToModel(out, &state)
	state.Organization = orgVal

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ProjectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan ProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state ProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot update project because the current state has no ID.")
		return
	}

	org := state.Organization.ValueString()

	// Build partial update payload
	var payload ProjectUpdate
	if !plan.ProjectName.IsNull() && !plan.ProjectName.IsUnknown() {
		v := plan.ProjectName.ValueString()
		payload.ProjectName = &v
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		v := plan.Description.ValueString()
		payload.Description = &v
	}

	out, err := r.client.UpdateProject(ctx, org, state.ProjectName.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update project failed", err.Error())
		return
	}

	var newState ProjectModel
	newState.Organization = state.Organization
	projectReadToModel(out, &newState)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *ProjectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()

	if err := r.client.DeleteProject(ctx, org, state.ProjectName.ValueString()); err != nil {
		// If already gone, treat as successful delete but log warning
		resp.Diagnostics.AddWarning("Delete project", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *ProjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Support "organization/id" (preferred) or just "id" (legacy).
	parts := strings.Split(req.ID, "/")
	switch len(parts) {
	case 2:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_name"), parts[1])...)
	case 1:
		resource.ImportStatePassthroughID(ctx, path.Root("project_name"), req, resp)
	default:
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "organization/project_name" or "project_name".`,
		)
	}
}
