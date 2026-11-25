// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

type ProjectResource struct{ configured }

type ProjectModel struct {
	ID             types.String `tfsdk:"id"`
	Organization   types.String `tfsdk:"organization"`
	OrganizationID types.String `tfsdk:"organization_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Visibility     types.String `tfsdk:"visibility"`
}

func (r *ProjectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *ProjectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire project. By default the credential creates the project in its own organization and must hold `organization:create_project`. On a self-hosted instance, an instance-admin credential creates the project in another organization with the `organization` argument or the provider `organization`.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Opaque backend project ID (not the slug).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Organization name. Defaults to the provider `organization`, else the organization of the provider credential. Setting it acts in that organization through the instance-admin token exchange (self-hosted only). Changing it replaces the project when the new name is another existing organization than `organization_id`. Logfire cannot move a project between organizations, so a replacement deletes the project with all its data and creates an empty project. Set `lifecycle { prevent_destroy = true }` to block it. A new name for the same organization, a name that is not known yet, or a name the instance does not list yet updates in place. After an organization is renamed, a refresh updates this value.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          organizationNameValidators(),
			},
			"organization_id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the organization that the project is in. Set when the project is in a named organization, through the `organization` argument or the provider `organization`. Null for a project in the organization of the credential.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
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
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"visibility": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Project visibility (`public` or `private`).",
				Default:             stringdefault.StaticString("public"),
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          []validator.String{stringvalidator.OneOf("public", "private")},
			},
		},
	}
}

func projectReadToModel(p *logclient.ProjectRead, m *ProjectModel) {
	m.ID = types.StringValue(p.ID)
	m.Organization = stringOrNull(&p.OrganizationName)
	m.Name = types.StringValue(p.ProjectName)
	m.Description = stringOrNull(p.Description)
	m.Visibility = stringOrNull(&p.Visibility)
}

func (r *ProjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	client := r.client
	if knownString(plan.Organization) {
		client = r.client.ForOrganization(plan.Organization.ValueString())
	}
	// Look the organization up before the create, so that a missing one fails
	// before anything exists.
	organizationID := types.StringNull()
	if name := client.Organization(); name != "" {
		id, ok := r.organizationID(ctx, name, "Create project failed", &resp.Diagnostics)
		if !ok {
			return
		}
		organizationID = types.StringValue(id)
	}

	out, err := client.CreateProject(ctx, logclient.ProjectCreate{
		ProjectName: plan.Name.ValueString(),
		Description: plan.Description.ValueStringPointer(),
		Visibility:  plan.Visibility.ValueStringPointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Create project failed", err.Error())
		return
	}

	var state ProjectModel
	projectReadToModel(out, &state)
	state.OrganizationID = organizationID

	tflog.Trace(ctx, "created project", map[string]any{
		"id": state.ID.ValueString(), "org": state.Organization.ValueString(),
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ProjectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	getProject := func(client *logclient.APIClient) (*logclient.ProjectRead, int, error) {
		out, status, err := client.GetProject(ctx, state.ID.ValueString())
		// The server answers 403 with this message for a project outside the
		// token's organization. An exchanged token has the scopes the provider
		// asked for, so with one the 403 means the project is not in the
		// organization. Any other 403, from a proxy for example, stays an error.
		if status == http.StatusForbidden && knownString(state.OrganizationID) &&
			strings.Contains(err.Error(), "You do not have permission to access this project") {
			status = http.StatusNotFound
		}
		return out, status, err
	}
	client := r.clientFor(state.Organization, state.OrganizationID)
	out, status, err := getProject(client)
	if knownString(state.OrganizationID) &&
		(logclient.IsInvalidTargetError(err) || status == http.StatusNotFound) {
		// The name may no longer belong to the project's organization: it was
		// renamed, possibly with another organization taking the old name, or it
		// was deleted with its projects. The ID tells which.
		id := state.OrganizationID.ValueString()
		current, lookupErr := findOrganization(ctx, r.client,
			func(o logclient.OrganizationRead) bool { return o.ID == id })
		switch {
		case lookupErr != nil:
			resp.Diagnostics.AddError("Read project failed", fmt.Sprintf(
				"%v. Listing the organizations to follow a rename failed: %v",
				err, lookupErr))
			return
		case current == nil:
			resp.State.RemoveResource(ctx)
			return
		case current.OrganizationName != state.Organization.ValueString():
			client = r.client.ForOrganization(current.OrganizationName)
			out, status, err = getProject(client)
		case logclient.IsInvalidTargetError(err):
			resp.Diagnostics.AddError("Read project failed", exchangeCause(err).Error())
			return
		}
	}
	if status == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read project failed", err.Error())
		return
	}

	projectReadToModel(out, &state)
	if name := client.Organization(); name != "" && !knownString(state.OrganizationID) {
		id, ok := r.organizationID(ctx, name, "Read project failed", &resp.Diagnostics)
		if !ok {
			return
		}
		state.OrganizationID = types.StringValue(id)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ProjectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	payload := logclient.ProjectUpdate{ProjectName: plan.Name.ValueStringPointer()}
	if !plan.Description.Equal(state.Description) {
		// The API clears a description when it gets an empty string.
		payload.Description = logclient.NullableFieldValue(plan.Description.ValueString())
	}
	if !plan.Visibility.Equal(state.Visibility) {
		payload.Visibility = logclient.NullableFieldValue(plan.Visibility.ValueString())
	}

	client := r.clientFor(plan.Organization, state.OrganizationID)
	if knownString(plan.Organization) && !plan.Organization.Equal(state.Organization) {
		client = r.client.ForOrganization(plan.Organization.ValueString())
	}
	out, err := client.UpdateProject(ctx, state.ID.ValueString(), payload)
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
	var state ProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.clientFor(state.Organization, state.OrganizationID).
		DeleteProject(ctx, state.ID.ValueString())
	// A project that is already gone is a successful delete.
	if err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Delete project failed", err.Error())
	}
}

func (r *ProjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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
			`Expected "organization/name" (also accepts "organization,name" or "organization|name"), or a single project UUID/name. Example:`+"\n"+
				`terraform import logfire_project.prod "acme/prod-logs"`,
		)
		return
	}

	var projectID string
	client := r.client
	if len(parts) == 2 {
		projectID, client, err = r.findProject(ctx, parts[0], parts[1])
		if err != nil {
			resp.Diagnostics.AddError("Lookup project ID failed", err.Error())
			return
		}
	} else {
		projectID, _, err = findProjectByNameOrID(ctx, r.client, parts[0])
		var notFound *projectNotFoundError
		switch {
		case errors.As(err, &notFound):
			resp.Diagnostics.AddError("Import project failed", fmt.Sprintf(
				"Project %q not found. Use the project UUID or the \"organization/name\" shorthand.", rawID))
			return
		case err != nil:
			resp.Diagnostics.AddError("Import project failed", err.Error())
			return
		}
	}

	project, _, err := client.GetProject(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Import project failed",
			fmt.Sprintf("fetching project %q: %v", projectID, err))
		return
	}

	var state ProjectModel
	projectReadToModel(project, &state)
	if client.Organization() != "" {
		id, ok := r.organizationID(
			ctx, project.OrganizationName, "Import project failed", &resp.Diagnostics)
		if !ok {
			return
		}
		state.OrganizationID = types.StringValue(id)
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
	// The last candidate is the target organization, so its failure is the answer
	// when nothing is found.
	var lastErr, ownErr error
	ownListed := false
	for i, c := range candidates {
		list, err := c.ListProjects(ctx)
		lastErr = err
		if i == 0 && len(candidates) > 1 {
			ownErr = err
		}
		ownListed = ownListed || (i == 0 && err == nil)
		for _, project := range list {
			if project.ProjectName == name && project.OrganizationName == org {
				return project.ID, c, nil
			}
		}
	}
	var adminRequired *logclient.InstanceAdminRequiredError
	if len(candidates) > 1 && ownListed && errors.As(lastErr, &adminRequired) {
		// The key's own list has no match, so the missing instance-admin right
		// matters only if org is not the key's own organization.
		return "", nil, fmt.Errorf("project %q was not found with this key; if %q is another organization, an instance-admin key is needed", name, org)
	}
	if lastErr != nil {
		// When the key's own list failed too, its error, such as a missing
		// scope, matters as much as the exchange's.
		return "", nil, errors.Join(ownErr, lastErr)
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

// ModifyPlan replaces the project only when the planned organization name is
// known and the instance list gives it another ID than organization_id. Any other
// change, an unknown name, a name the list does not have yet, or a rename of the
// same organization, updates in place. A name that is missing only because the
// same apply creates or renames the organization must not delete the project, and
// Update fails without deleting anything when the project is not in the planned
// organization.
func (r *ProjectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan ProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Organization.IsUnknown() ||
		plan.Organization.Equal(state.Organization) {
		return
	}
	name := plan.Organization.ValueString()
	found, err := findOrganization(ctx, r.client,
		func(o logclient.OrganizationRead) bool { return o.OrganizationName == name })
	if err != nil || found == nil || found.ID == state.OrganizationID.ValueString() {
		return
	}
	// The replacement deletes the project before it creates the new one, so
	// check now that the provider can act in the new organization.
	existing, err := r.client.ForOrganization(name).ListProjects(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot move the project", fmt.Sprintf(
			"Moving the project to organization %q replaces it, but the provider cannot act in that organization: %v", name, err))
		return
	}
	// A project with a null organization_id is in the key's organization, so a
	// name for that organization, its own or a new one after a rename, is no move.
	nameTaken := false
	for _, p := range existing {
		if p.ID == state.ID.ValueString() {
			return
		}
		nameTaken = nameTaken || p.ProjectName == plan.Name.ValueString()
	}
	if nameTaken {
		resp.Diagnostics.AddError("Cannot move the project", fmt.Sprintf(
			"Organization %q already has a project named %q, so the replacement would delete this project and then fail to create it.",
			name, plan.Name.ValueString()))
		return
	}
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("organization"))
}

// findOrganization returns the first organization of the instance list that
// match accepts, or nil. The list comes from the instance route, which takes the
// API key itself.
func findOrganization(ctx context.Context, c *logclient.APIClient, match func(logclient.OrganizationRead) bool) (*logclient.OrganizationRead, error) {
	list, err := c.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(list, match)
	if i < 0 {
		return nil, nil
	}
	return &list[i], nil
}

// organizationID looks up the ID that the instance list gives an organization
// name. When it finds none, it adds the diagnostic itself.
func (r *ProjectResource) organizationID(ctx context.Context, name, summary string, diags *diag.Diagnostics) (string, bool) {
	found, err := findOrganization(ctx, r.client,
		func(o logclient.OrganizationRead) bool { return o.OrganizationName == name })
	switch {
	case err != nil:
		diags.AddError(summary, fmt.Sprintf(
			"Listing the instance organizations to find %q failed: %v", name, err))
	case found == nil:
		diags.AddError(summary, fmt.Sprintf(
			"Organization %q does not exist on this instance.", name))
	default:
		return found.ID, true
	}
	return "", false
}

func knownString(v types.String) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueString() != ""
}
