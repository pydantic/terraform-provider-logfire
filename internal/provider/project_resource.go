// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ resource.Resource = &ProjectResource{}
var _ resource.ResourceWithConfigure = &ProjectResource{}
var _ resource.ResourceWithImportState = &ProjectResource{}
var _ resource.ResourceWithModifyPlan = &ProjectResource{}

func NewProjectResource() resource.Resource { return &ProjectResource{} }

type ProjectResource struct {
	client *logclient.APIClient
}

type ProjectModel struct {
	ID             types.String `tfsdk:"id"`
	Organization   types.String `tfsdk:"organization"`
	OrganizationID types.String `tfsdk:"organization_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Visibility     types.String `tfsdk:"visibility"`
}

func (r *ProjectResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *ProjectResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire project. By default the credential creates the project in its own organization and must hold `organization:create_project`. " +
			"On a self-hosted instance, an instance-admin credential creates the project in another organization with the `organization` argument or the provider `organization`.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Opaque backend project ID (not the slug).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization": rschema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Organization name. Defaults to the provider `organization`, else the organization of the provider credential. " +
					"Setting it acts in that organization through the instance-admin token exchange (self-hosted only). " +
					"Changing it replaces the project, unless the new name belongs to the same organization, which Terraform checks by `organization_id`. " +
					"After an organization is renamed, a refresh updates this value.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: organizationNameValidators(),
			},
			"organization_id": rschema.StringAttribute{
				Computed: true,
				MarkdownDescription: "ID of the organization that the project is in. Set when the project is in a named organization, " +
					"through the `organization` argument or the provider `organization`. Null for a project in the organization of the credential.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project name/slug. Must be unique within the organization.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(2),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`),
						"must use lowercase letters, numbers, and internal hyphens (no spaces or uppercase characters)",
					),
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
	c, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *APIClient, got %T", req.ProviderData))
		return
	}
	r.client = c
}

// --- Helpers ---

func projectModelToCreate(m *ProjectModel) logclient.ProjectCreate {
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
	return logclient.ProjectCreate{
		ProjectName: m.Name.ValueString(),
		Description: desc,
		Visibility:  visibility,
	}
}

func projectReadToModel(p *logclient.ProjectRead, m *ProjectModel) {
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

	client := r.client
	if knownString(plan.Organization) {
		client = r.client.ForOrganization(plan.Organization.ValueString())
	}
	// Look the organization up before the create, so that a missing one fails
	// before anything exists.
	organizationID := types.StringNull()
	if name := client.Organization(); name != "" {
		found, err := findOrganization(ctx, r.client, func(o logclient.OrganizationRead) bool { return o.OrganizationName == name })
		if err != nil || found == nil {
			resp.Diagnostics.AddError("Create project failed", organizationLookupMessage(name, err))
			return
		}
		organizationID = types.StringValue(found.ID)
	}

	out, err := client.CreateProject(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create project failed", err.Error())
		return
	}

	var state ProjectModel
	projectReadToModel(out, &state)
	state.OrganizationID = organizationID

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

	if state.ID.IsUnknown() || state.ID.IsNull() || state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	client := r.clientFor(state.Organization, state.OrganizationID)
	out, status, err := client.GetProject(ctx, state.ID.ValueString())
	if logclient.IsInvalidTargetError(err) && knownString(state.OrganizationID) {
		// The exchange knows no organization of this name. Either the organization
		// was renamed, and the ID gives its new name, or it was deleted with its
		// projects.
		id := state.OrganizationID.ValueString()
		current, lookupErr := findOrganization(ctx, r.client, func(o logclient.OrganizationRead) bool { return o.ID == id })
		switch {
		case lookupErr != nil:
			resp.Diagnostics.AddError("Read project failed", fmt.Sprintf("%v. Listing the organizations to follow a rename failed: %v", err, lookupErr))
			return
		case current == nil:
			resp.State.RemoveResource(ctx)
			return
		case current.OrganizationName == state.Organization.ValueString():
			resp.Diagnostics.AddError("Read project failed", err.Error())
			return
		}
		client = r.client.ForOrganization(current.OrganizationName)
		out, status, err = client.GetProject(ctx, state.ID.ValueString())
	}
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read project failed", err.Error())
		return
	}

	projectReadToModel(out, &state)
	if name := client.Organization(); name != "" && !knownString(state.OrganizationID) {
		found, err := findOrganization(ctx, r.client, func(o logclient.OrganizationRead) bool { return o.OrganizationName == name })
		if err != nil || found == nil {
			resp.Diagnostics.AddError("Read project failed", organizationLookupMessage(name, err))
			return
		}
		state.OrganizationID = types.StringValue(found.ID)
	}

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
	var payload logclient.ProjectUpdate
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		v := plan.Name.ValueString()
		payload.ProjectName = &v
	}

	if !plan.Description.IsUnknown() {
		switch {
		case plan.Description.IsNull():
			if !state.Description.IsNull() && !state.Description.IsUnknown() {
				// API clears descriptions when provided an empty string.
				payload.Description = logclient.NullableFieldValue("")
			}
		default:
			v := plan.Description.ValueString()
			if state.Description.IsNull() || state.Description.IsUnknown() || state.Description.ValueString() != v {
				payload.Description = logclient.NullableFieldValue(v)
			}
		}
	}

	if !plan.Visibility.IsUnknown() {
		switch {
		case plan.Visibility.IsNull():
			if !state.Visibility.IsNull() && !state.Visibility.IsUnknown() {
				payload.Visibility = logclient.NullableFieldNull[string]()
			}
		default:
			v := plan.Visibility.ValueString()
			if state.Visibility.IsNull() || state.Visibility.IsUnknown() || state.Visibility.ValueString() != v {
				payload.Visibility = logclient.NullableFieldValue(v)
			}
		}
	}

	out, err := r.clientFor(plan.Organization, state.OrganizationID).UpdateProject(ctx, state.ID.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update project failed", err.Error())
		return
	}

	var newState ProjectModel
	projectReadToModel(out, &newState)
	newState.OrganizationID = state.OrganizationID
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

	if err := r.clientFor(state.Organization, state.OrganizationID).DeleteProject(ctx, state.ID.ValueString()); err != nil {
		if logclient.IsNotFoundError(err) {
			// Already gone, treat as successful delete
			return
		}
		resp.Diagnostics.AddError("Delete project failed", err.Error())
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

	parts, err := splitImportParts(rawID, 1, 2)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			`Expected "organization/name" (also accepts "organization,name" or "organization|name"), or a single project UUID/name. Example:
terraform import logfire_project.prod "acme/prod-logs"`,
		)
		return
	}

	projectID := ""
	client := r.client
	switch len(parts) {
	case 2:
		org, name := parts[0], parts[1]
		id, found, err := r.findProject(ctx, org, name)
		if err != nil {
			resp.Diagnostics.AddError("Lookup project ID failed", err.Error())
			return
		}
		projectID, client = id, found
	case 1:
		id, _, err := findProjectByNameOrID(ctx, r.client, parts[0])
		if err != nil {
			resp.Diagnostics.AddError(
				"Import project failed",
				fmt.Sprintf("Project %q not found. Use the project UUID or the \"organization/name\" shorthand.", rawID),
			)
			return
		}
		projectID = id
	}

	project, _, err := client.GetProject(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Import project failed", fmt.Sprintf("fetching project %q: %v", projectID, err))
		return
	}

	var state ProjectModel
	projectReadToModel(project, &state)
	if client.Organization() != "" {
		found, err := findOrganization(ctx, r.client, func(o logclient.OrganizationRead) bool { return o.OrganizationName == project.OrganizationName })
		if err != nil || found == nil {
			resp.Diagnostics.AddError("Import project failed", organizationLookupMessage(project.OrganizationName, err))
			return
		}
		state.OrganizationID = types.StringValue(found.ID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// findProject looks a project up by organization and name and returns its ID and
// the client that reaches it. Without a provider organization the lookup starts
// with the credential's own organization, so that a key without instance-admin
// rights still imports its own projects, and then tries the named organization
// through the exchange.
func (r *ProjectResource) findProject(ctx context.Context, org, name string) (string, *logclient.APIClient, error) {
	candidates := []*logclient.APIClient{r.client}
	switch r.client.Organization() {
	case org:
	case "":
		candidates = append(candidates, r.client.ForOrganization(org))
	default:
		candidates = []*logclient.APIClient{r.client.ForOrganization(org)}
	}
	var lastErr error
	listed := false
	for _, c := range candidates {
		list, err := c.ListProjects(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		listed = true
		for _, project := range list {
			if project.ProjectName == name && project.OrganizationName == org {
				return project.ID, c, nil
			}
		}
	}
	if !listed {
		return "", nil, lastErr
	}
	return "", nil, fmt.Errorf("project %q/%q not found", org, name)
}

// clientFor returns the client that reaches a project that is already in state.
// A project in a named organization stays there, whatever the provider
// organization is now.
func (r *ProjectResource) clientFor(organization, organizationID types.String) *logclient.APIClient {
	if knownString(organization) && knownString(organizationID) {
		return r.client.ForOrganization(organization.ValueString())
	}
	return r.client
}

// ModifyPlan decides whether a new organization name replaces the project. A
// name that resolves to the organization of organization_id is a rename and
// updates in place. A name that no organization has fails the plan, because
// Terraform would delete the project before the create failed. A name that is
// not known yet replaces, as any changed value does.
func (r *ProjectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var prior, planned, priorID types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("organization"), &prior)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("organization"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("organization_id"), &priorID)...)
	if resp.Diagnostics.HasError() || planned.Equal(prior) {
		return
	}
	organization := path.Root("organization")
	if planned.IsUnknown() {
		resp.RequiresReplace = append(resp.RequiresReplace, organization)
		return
	}
	name := planned.ValueString()
	found, err := findOrganization(ctx, r.client, func(o logclient.OrganizationRead) bool { return o.OrganizationName == name })
	if err != nil || found == nil {
		resp.Diagnostics.AddAttributeError(organization, "Cannot replace the project",
			organizationLookupMessage(name, err)+" Terraform stops here, because it would delete the project before the create failed. "+
				"If the organization was renamed, set organization to its new name.")
		return
	}
	if !knownString(priorID) || found.ID != priorID.ValueString() {
		resp.RequiresReplace = append(resp.RequiresReplace, organization)
	}
}

// findOrganization returns the first organization of the instance list that
// match accepts, or nil. The list comes from the instance route, which takes the
// API key itself.
func findOrganization(ctx context.Context, c *logclient.APIClient, match func(logclient.OrganizationRead) bool) (*logclient.OrganizationRead, error) {
	list, err := c.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if match(list[i]) {
			return &list[i], nil
		}
	}
	return nil, nil
}

func organizationLookupMessage(name string, err error) string {
	if err != nil {
		return fmt.Sprintf("Listing the instance organizations to find %q failed: %v", name, err)
	}
	return fmt.Sprintf("Organization %q does not exist on this instance.", name)
}

func knownString(v types.String) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueString() != ""
}
