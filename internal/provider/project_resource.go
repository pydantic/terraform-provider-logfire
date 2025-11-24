// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
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
	Visibility   types.String `tfsdk:"visibility"`
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
				Computed:            true,
				MarkdownDescription: "Organization name. Computed from the API and cannot be set.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(2),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project name/slug. Must be unique within the organization.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(2),
				},
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Project description.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"visibility": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Project visibility (`public` or `private`).",
				Default:             stringdefault.StaticString("public"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("public", "private"),
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

// --- Helpers ---

func projectModelToCreate(m *ProjectModel) ProjectCreate {
	var desc *string
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		value := m.Description.ValueString()
		if value != "" {
			desc = &value
		}
	}
	var visibility *string
	if !m.Visibility.IsNull() && !m.Visibility.IsUnknown() {
		value := m.Visibility.ValueString()
		visibility = &value
	}
	return ProjectCreate{
		ProjectName: m.Name.ValueString(),
		Description: desc,
		Visibility:  visibility,
	}
}

func projectReadToModel(p *ProjectRead, m *ProjectModel) {
	m.ID = types.StringValue(p.ID)
	if p.OrganizationName == "" {
		m.Organization = types.StringNull()
	} else {
		m.Organization = types.StringValue(p.OrganizationName)
	}
	m.Name = types.StringValue(p.ProjectName)
	if p.Description == nil || *p.Description == "" {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*p.Description)
	}
	if p.Visibility == "" {
		m.Visibility = types.StringNull()
	} else {
		m.Visibility = types.StringValue(p.Visibility)
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

	in := projectModelToCreate(&plan)

	out, err := r.client.CreateProject(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create project failed", err.Error())
		return
	}

	var state ProjectModel
	projectReadToModel(out, &state)

	tflog.Trace(ctx, "created project", map[string]any{"id": state.ID.ValueString(), "org": state.Organization.ValueString()})
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

	if state.ID.IsUnknown() || state.ID.IsNull() {
		resp.Diagnostics.AddError("Missing ID", "Cannot read project because the state is missing an ID.")
		return
	}

	out, status, err := r.client.GetProject(ctx, state.ID.ValueString())
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

	if !plan.Description.IsUnknown() {
		switch {
		case plan.Description.IsNull():
			if !state.Description.IsNull() && !state.Description.IsUnknown() {
				// API clears descriptions when provided an empty string.
				payload.Description = NullableFieldValue("")
			}
		default:
			v := plan.Description.ValueString()
			if state.Description.IsNull() || state.Description.IsUnknown() || state.Description.ValueString() != v {
				payload.Description = NullableFieldValue(v)
			}
		}
	}

	if !plan.Visibility.IsUnknown() {
		switch {
		case plan.Visibility.IsNull():
			if !state.Visibility.IsNull() && !state.Visibility.IsUnknown() {
				payload.Visibility = NullableFieldNull[string]()
			}
		default:
			v := plan.Visibility.ValueString()
			if state.Visibility.IsNull() || state.Visibility.IsUnknown() || state.Visibility.ValueString() != v {
				payload.Visibility = NullableFieldValue(v)
			}
		}
	}

	out, err := r.client.UpdateProject(ctx, state.ID.ValueString(), payload)
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

	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot delete project because the current state has no ID.")
		return
	}

	if err := r.client.DeleteProject(ctx, state.ID.ValueString()); err != nil {
		if isRateLimitError(err) {
			resp.Diagnostics.AddError("Delete project", fmt.Sprintf("rate limited while deleting project: %v", err))
			return
		}
		// If already gone, treat as successful delete but log warning
		resp.Diagnostics.AddWarning("Delete project", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *ProjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	rawID := strings.TrimSpace(req.ID)
	if rawID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use either the project UUID or the "organization/name" shorthand.`,
		)
		return
	}

	projectID := rawID

	if strings.ContainsAny(rawID, "/,|") {
		org, name, ok := splitTwo(rawID)
		if !ok || org == "" || name == "" {
			resp.Diagnostics.AddError(
				"Invalid import ID format",
				`Expected "organization/name" (also accepts "organization,name" or "organization|name"). Example:
terraform import logfire_project.prod "acme/prod-logs"`,
			)
			return
		}

		id, err := r.findProjectID(ctx, org, name)
		if err != nil {
			resp.Diagnostics.AddError("Lookup project ID failed", err.Error())
			return
		}
		projectID = id
	}

	project, _, err := r.client.GetProject(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Import project failed", fmt.Sprintf("fetching project %q: %v", projectID, err))
		return
	}

	var state ProjectModel
	projectReadToModel(project, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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

func (r *ProjectResource) findProjectID(ctx context.Context, org, name string) (string, error) {
	list, err := r.client.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, project := range list {
		if project.ProjectName == name && project.OrganizationName == org {
			return project.ID, nil
		}
	}
	return "", fmt.Errorf("project %q/%q not found", org, name)
}
