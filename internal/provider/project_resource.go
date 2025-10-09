// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var _ resource.Resource = &ProjectResource{}
var _ resource.ResourceWithConfigure = &ProjectResource{}
var _ resource.ResourceWithImportState = &ProjectResource{}

func NewProjectResource() resource.Resource { return &ProjectResource{} }

type ProjectResource struct {
	client *APIClient
}

type ProjectModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	Name         types.String `tfsdk:"name"`
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
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project name/slug. Must be unique within the organization.",
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Project description.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
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
	m.Organization = types.StringValue(p.OrganizationName)
	m.Name = types.StringValue(p.ProjectName)
	if p.Description == "" {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(p.Description)
	}
}

func projectModelToCreate(m *ProjectModel) ProjectCreate {
	return ProjectCreate{
		ProjectName: m.Name.ValueString(),
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
	projectReadToModel(out, &state)

	tflog.Trace(ctx, "created project", map[string]any{"id": state.ID.ValueString(), "org": org})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
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

	out, status, err := r.client.GetProject(ctx, state.Organization.ValueString(), state.Name.ValueString())
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read project failed", err.Error())
		return
	}

	projectReadToModel(out, &state)

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

	// Build partial update payload
	var payload ProjectUpdate
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		v := plan.Name.ValueString()
		payload.ProjectName = &v
	}

	switch {
	case !plan.Description.IsNull() && !plan.Description.IsUnknown():
		v := plan.Description.ValueString()
		payload.Description = &v
	case plan.Description.IsNull() && !state.Description.IsNull() && !state.Description.IsUnknown():
		empty := ""
		payload.Description = &empty
	}

	out, err := r.client.UpdateProject(ctx, state.Organization.ValueString(), state.Name.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update project failed", err.Error())
		return
	}

	var newState ProjectModel
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

	if err := r.client.DeleteProject(ctx, state.Organization.ValueString(), state.Name.ValueString()); err != nil {
		// If already gone, treat as successful delete but log warning
		resp.Diagnostics.AddWarning("Delete project", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *ProjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use: terraform import logfire_project.prod "organization/name"`,
		)
		return
	}

	org, name, ok := splitTwo(req.ID)
	if !ok || org == "" || name == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			`Expected "organization/name" (also accepts "organization,name" or "organization|name"). Example:
terraform import logfire_project.prod "acme/prod-logs"`,
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), org)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)

}

func splitTwo(s string) (string, string, bool) {
	// Accept a few common separators to be user-friendly
	seps := []string{"/", ",", "|"}
	for _, sep := range seps {
		if parts := strings.SplitN(s, sep, 2); len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
		}
	}
	return "", "", false
}
