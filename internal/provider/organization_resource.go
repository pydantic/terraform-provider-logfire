// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ resource.Resource = &OrganizationResource{}
var _ resource.ResourceWithConfigure = &OrganizationResource{}
var _ resource.ResourceWithImportState = &OrganizationResource{}

func NewOrganizationResource() resource.Resource { return &OrganizationResource{} }

type OrganizationResource struct {
	client *logclient.APIClient
}

var organizationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type OrganizationModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	DisplayName        types.String `tfsdk:"display_name"`
	BillingEmail       types.String `tfsdk:"billing_email"`
	GithubHandle       types.String `tfsdk:"github_handle"`
	Location           types.String `tfsdk:"location"`
	Avatar             types.String `tfsdk:"avatar"`
	Description        types.String `tfsdk:"description"`
	HasAdminPanel      types.Bool   `tfsdk:"has_admin_panel"`
	GatewayEnabled     types.Bool   `tfsdk:"gateway_enabled"`
	AIEnabled          types.Bool   `tfsdk:"ai_enabled"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

func (r *OrganizationResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (r *OrganizationResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire organization. This resource is only available for self-hosted deployments and requires an API key with a special organization scope.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Organization UUID assigned by the backend.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization name/slug.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(2),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`),
						"must use lowercase letters, numbers, and internal hyphens (no spaces or uppercase characters)",
					),
				},
			},
			"display_name": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Display name shown in the UI.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"billing_email": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Billing contact email.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"github_handle": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Organization GitHub handle.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"location": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Organization location.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"avatar": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Avatar URL.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Organization description.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"has_admin_panel": rschema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the organization has access to the admin panel.",
			},
			"gateway_enabled": rschema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether gateway features are enabled.",
			},
			"ai_enabled": rschema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether AI features are enabled.",
			},
			"created_at": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the organization was created.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the organization was last updated.",
			},
			"deletion_protection": rschema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Prevents accidental destroy when true (defaults to true). Set to false and apply before deleting this resource.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *OrganizationResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *OrganizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan OrganizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in := logclient.OrganizationCreate{
		OrganizationName:        plan.Name.ValueString(),
		OrganizationDisplayName: terraformStringPointer(plan.DisplayName),
		GithubHandle:            terraformStringPointer(plan.GithubHandle),
		Location:                terraformStringPointer(plan.Location),
		Avatar:                  terraformStringPointer(plan.Avatar),
		Description:             terraformStringPointer(plan.Description),
	}

	out, err := r.client.CreateOrganization(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create organization failed", err.Error())
		return
	}

	if billingEmail := terraformStringPointer(plan.BillingEmail); billingEmail != nil {
		updated, updateErr := r.client.UpdateOrganization(ctx, out.ID, logclient.OrganizationUpdate{
			BillingEmail: billingEmail,
		})
		if updateErr != nil {
			resp.Diagnostics.AddError("Create organization failed", fmt.Sprintf("setting billing_email: %v", updateErr))
			return
		}
		out = updated
	}

	var state OrganizationModel
	organizationReadToModel(out, &state)
	state.DeletionProtection = normalizeDeletionProtection(plan.DeletionProtection)

	tflog.Trace(ctx, "created organization", map[string]any{"id": state.ID.ValueString(), "name": state.Name.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state OrganizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot read organization because the state is missing an ID.")
		return
	}

	currentDeletionProtection := normalizeDeletionProtection(state.DeletionProtection)

	out, status, err := r.client.GetOrganization(ctx, state.ID.ValueString())
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read organization failed", err.Error())
		return
	}

	organizationReadToModel(out, &state)
	state.DeletionProtection = currentDeletionProtection
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan OrganizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state OrganizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot update organization because the current state has no ID.")
		return
	}

	payload := logclient.OrganizationUpdate{}
	changed := false

	if !plan.Name.IsUnknown() && !plan.Name.IsNull() {
		desiredName := plan.Name.ValueString()
		if state.Name.IsNull() || state.Name.IsUnknown() || state.Name.ValueString() != desiredName {
			payload.OrganizationName = &desiredName
			changed = true
		}
	}

	if updateStringField(plan.DisplayName, state.DisplayName, &payload.OrganizationDisplayName) {
		changed = true
	}
	if updateStringField(plan.BillingEmail, state.BillingEmail, &payload.BillingEmail) {
		changed = true
	}
	if updateStringField(plan.GithubHandle, state.GithubHandle, &payload.GithubHandle) {
		changed = true
	}
	if updateStringField(plan.Location, state.Location, &payload.Location) {
		changed = true
	}
	if updateStringField(plan.Avatar, state.Avatar, &payload.Avatar) {
		changed = true
	}
	if updateStringField(plan.Description, state.Description, &payload.Description) {
		changed = true
	}

	if !changed {
		state.DeletionProtection = normalizeDeletionProtection(plan.DeletionProtection)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	out, err := r.client.UpdateOrganization(ctx, state.ID.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update organization failed", err.Error())
		return
	}

	var newState OrganizationModel
	organizationReadToModel(out, &newState)
	newState.DeletionProtection = normalizeDeletionProtection(plan.DeletionProtection)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *OrganizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state OrganizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot delete organization because the current state has no ID.")
		return
	}

	if normalizeDeletionProtection(state.DeletionProtection).ValueBool() {
		resp.Diagnostics.AddError(
			"Organization deletion is protected",
			"This resource has `deletion_protection = true`. Set `deletion_protection = false` and apply once before destroying the organization.",
		)
		return
	}

	if err := r.client.DeleteOrganization(ctx, state.ID.ValueString()); err != nil {
		if logclient.IsNotFoundError(err) {
			// Already gone, treat as successful delete.
			return
		}
		resp.Diagnostics.AddError("Delete organization failed", err.Error())
	}
}

func (r *OrganizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	rawID := strings.TrimSpace(req.ID)
	if rawID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use either organization UUID or organization name.`,
		)
		return
	}

	parts, err := splitImportParts(rawID, 1)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			`Expected a single organization UUID or organization name.`,
		)
		return
	}

	out, found, err := r.findOrganizationByNameOrID(ctx, parts[0])
	if err != nil {
		resp.Diagnostics.AddError("Import organization failed", err.Error())
		return
	}
	if !found {
		resp.Diagnostics.AddError("Import organization failed", fmt.Sprintf("organization %q not found", parts[0]))
		return
	}

	var state OrganizationModel
	organizationReadToModel(out, &state)
	state.DeletionProtection = types.BoolValue(true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationResource) findOrganizationByNameOrID(ctx context.Context, key string) (*logclient.OrganizationRead, bool, error) {
	if organizationIDPattern.MatchString(key) {
		out, status, err := r.client.GetOrganization(ctx, key)
		if err == nil {
			return out, true, nil
		}
		if status != 404 {
			return nil, false, err
		}
	}

	list, err := r.client.ListOrganizations(ctx)
	if err != nil {
		return nil, false, err
	}
	for i := range list {
		if list[i].ID == key || list[i].OrganizationName == key {
			return &list[i], true, nil
		}
	}
	return nil, false, nil
}

func organizationReadToModel(o *logclient.OrganizationRead, m *OrganizationModel) {
	m.ID = types.StringValue(o.ID)
	m.Name = types.StringValue(o.OrganizationName)
	m.DisplayName = optionalStringToTerraform(o.OrganizationDisplayName)
	m.BillingEmail = optionalStringToTerraform(o.BillingEmail)
	m.GithubHandle = optionalStringToTerraform(o.GithubHandle)
	m.Location = optionalStringToTerraform(o.Location)
	m.Avatar = optionalStringToTerraform(o.Avatar)
	m.Description = optionalStringToTerraform(o.Description)
	m.HasAdminPanel = types.BoolValue(o.HasAdminPanel)
	m.GatewayEnabled = types.BoolValue(o.GatewayEnabled)
	m.AIEnabled = types.BoolValue(o.AIEnabled)
	if o.CreatedAt == "" {
		m.CreatedAt = types.StringNull()
	} else {
		m.CreatedAt = types.StringValue(o.CreatedAt)
	}
	if o.UpdatedAt == "" {
		m.UpdatedAt = types.StringNull()
	} else {
		m.UpdatedAt = types.StringValue(o.UpdatedAt)
	}
}

func normalizeDeletionProtection(in types.Bool) types.Bool {
	if in.IsNull() || in.IsUnknown() {
		return types.BoolValue(true)
	}
	return in
}

func optionalStringToTerraform(in *string) types.String {
	if in == nil || strings.TrimSpace(*in) == "" {
		return types.StringNull()
	}
	return types.StringValue(*in)
}

func terraformStringPointer(in types.String) *string {
	if in.IsNull() || in.IsUnknown() {
		return nil
	}
	value := strings.TrimSpace(in.ValueString())
	if value == "" {
		return nil
	}
	return &value
}

func updateStringField(plan, state types.String, out **string) bool {
	if plan.IsUnknown() || plan.IsNull() {
		return false
	}
	desired := strings.TrimSpace(plan.ValueString())
	current := ""
	if !state.IsUnknown() && !state.IsNull() {
		current = state.ValueString()
	}
	if desired == current {
		return false
	}
	*out = &desired
	return true
}
