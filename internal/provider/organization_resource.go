// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

type OrganizationResource struct{ configured }

var uuidPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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

func (r *OrganizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (r *OrganizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire organization. This resource is only available for self-hosted deployments and requires an API key created in the admin organization (the one with the admin panel) carrying the `organization:admin` scope. A key minted inside another organization cannot manage organizations regardless of its scopes. Reading, updating, and deleting an organization authenticates with a short-lived organization-scoped token exchanged from that key, so every operation beyond creating and listing requires a Logfire backend from 2026-06-25 (v2026-06-25.01) or newer; creating and listing organizations require 2026-06-03 (v2026-06-03.01) or newer.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Organization UUID assigned by the backend.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization name/slug.",
				Validators:          organizationNameValidators(),
			},
			"display_name": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Display name shown in the UI.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"billing_email": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Logfire no longer stores a billing email for an organization. The provider does not send this value to Logfire and keeps the configured value only in state. It will be removed in v0.4.0.",
				DeprecationMessage:  "Logfire no longer stores an organization billing email, so the provider ignores billing_email. Remove it from the configuration. The attribute will be removed in v0.4.0.",
			},
			"github_handle": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Organization GitHub handle.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"location": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Organization location.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"avatar": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Avatar URL.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Organization description.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
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
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
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
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *OrganizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateOrganization(ctx, logclient.OrganizationCreate{
		OrganizationName:        plan.Name.ValueString(),
		OrganizationDisplayName: terraformStringPointer(plan.DisplayName),
		GithubHandle:            terraformStringPointer(plan.GithubHandle),
		Location:                terraformStringPointer(plan.Location),
		Avatar:                  terraformStringPointer(plan.Avatar),
		Description:             terraformStringPointer(plan.Description),
	})
	if err != nil {
		resp.Diagnostics.AddError("Create organization failed", err.Error())
		return
	}
	var state OrganizationModel
	organizationReadToModel(out, &state)
	// Logfire does not store billing_email, so state holds the planned value.
	state.BillingEmail = plan.BillingEmail
	state.DeletionProtection = plan.DeletionProtection

	tflog.Trace(ctx, "created organization", map[string]any{
		"id": state.ID.ValueString(), "name": state.Name.ValueString(),
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot read organization because the state is missing an ID.")
		return
	}

	out, gone := r.verifiedOrganization(
		ctx, &resp.Diagnostics, "Read organization failed", reimportHint, &state)
	if gone {
		resp.State.RemoveResource(ctx)
	}
	if out == nil {
		return
	}

	// Logfire does not return billing_email, so the read keeps the saved value.
	organizationReadToModel(out, &state)
	// State from before deletion_protection existed holds null, which protects.
	if state.DeletionProtection.IsNull() {
		state.DeletionProtection = types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state OrganizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	payload := logclient.OrganizationUpdate{
		OrganizationName:        trimmedIfChanged(plan.Name, state.Name),
		OrganizationDisplayName: trimmedIfChanged(plan.DisplayName, state.DisplayName),
		GithubHandle:            trimmedIfChanged(plan.GithubHandle, state.GithubHandle),
		Location:                trimmedIfChanged(plan.Location, state.Location),
		Avatar:                  trimmedIfChanged(plan.Avatar, state.Avatar),
		Description:             trimmedIfChanged(plan.Description, state.Description),
	}
	if payload == (logclient.OrganizationUpdate{}) {
		state.BillingEmail = plan.BillingEmail
		state.DeletionProtection = plan.DeletionProtection
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	current, gone := r.verifiedOrganization(
		ctx, &resp.Diagnostics, "Update organization failed", reimportHint, &state)
	if gone {
		resp.Diagnostics.AddError("Update organization failed",
			"the organization no longer exists; refresh the state before updating.")
	}
	if current == nil {
		return
	}

	// The audience is the organization's current (state) name; a rename is
	// carried in the payload, not in the audience.
	out, err := r.client.UpdateOrganizationContext(ctx, state.Name.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update organization failed", err.Error())
		return
	}
	if out.ID != state.ID.ValueString() {
		resp.Diagnostics.AddError("Update organization failed", fmt.Sprintf(
			"the update applied to organization id %s, but state holds id %s. Refusing to record the change; re-import the intended organization to re-bind state.", out.ID, state.ID.ValueString()))
		return
	}

	var newState OrganizationModel
	organizationReadToModel(out, &newState)
	newState.BillingEmail = plan.BillingEmail
	newState.DeletionProtection = plan.DeletionProtection
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *OrganizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.DeletionProtection.IsNull() || state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError(
			"Organization deletion is protected",
			"This resource has `deletion_protection = true`. Set `deletion_protection = false` and apply once before destroying the organization.",
		)
		return
	}

	current, _ := r.verifiedOrganization(ctx, &resp.Diagnostics,
		"Delete organization failed",
		"refusing to delete it. Re-import the intended organization to re-bind state.",
		&state)
	if current == nil {
		return
	}

	err := r.client.DeleteOrganizationContext(ctx, state.Name.ValueString())
	switch {
	case err == nil, logclient.IsNotFoundError(err):
		// Vanished between the identity check and the delete: already gone.
	case logclient.IsInvalidTargetError(err):
		r.appendOrganizationContextError(
			ctx, &resp.Diagnostics, "Delete organization failed", err, 0, &state)
	default:
		resp.Diagnostics.AddError("Delete organization failed", err.Error())
	}
}

func (r *OrganizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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

	list, err := r.client.ListOrganizations(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Import organization failed", err.Error())
		return
	}
	// An ID match is authoritative: organization names may themselves be
	// UUID-shaped, and a name-equals-ID coincidence must not win.
	i := slices.IndexFunc(list, func(o logclient.OrganizationRead) bool {
		return o.ID == parts[0]
	})
	if i < 0 {
		i = slices.IndexFunc(list, func(o logclient.OrganizationRead) bool {
			return o.OrganizationName == parts[0]
		})
	}
	if i < 0 {
		resp.Diagnostics.AddError("Import organization failed",
			fmt.Sprintf("organization %q not found", parts[0]))
		return
	}

	var state OrganizationModel
	organizationReadToModel(&list[i], &state)
	state.DeletionProtection = types.BoolValue(true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// exchangeCause returns the token endpoint's own answer. The client words
// invalid_target as an unknown organization, which is wrong where the caller has
// found the organization in the instance list.
func exchangeCause(err error) error {
	var oauthErr *logclient.OAuthTokenError
	if errors.As(err, &oauthErr) {
		return oauthErr
	}
	return err
}

const reimportHint = "re-import the intended organization to re-bind state."

// verifiedOrganization reads the organization that state names and checks that
// it still is the one state holds. The org-context API addresses organizations
// by name and resolves them from the exchanged token, so a reused name would act
// on an unrelated organization. It returns gone when the organization verifiably
// no longer exists, and the caller decides what that means. With neither a result
// nor gone, it has added a diagnostic. tail ends the message for a reused name.
func (r *OrganizationResource) verifiedOrganization(ctx context.Context, diags *diag.Diagnostics, summary, tail string, state *OrganizationModel) (current *logclient.OrganizationRead, gone bool) {
	current, status, err := r.client.GetOrganizationContext(ctx, state.Name.ValueString())
	if err != nil {
		return nil, r.appendOrganizationContextError(ctx, diags, summary, err, status, state)
	}
	if current.ID != state.ID.ValueString() {
		diags.AddError(summary, fmt.Sprintf(
			"the organization at name %q has id %s, but state holds id %s. The name was reused by a different organization; %s",
			state.Name.ValueString(), current.ID, state.ID.ValueString(), tail))
		return nil, false
	}
	return current, false
}

// appendOrganizationContextError appends diagnostics for a failed org-context
// operation. An invalid_target audience means a missing organization, an
// externally renamed one, a reused name, or an audience that does not match the
// instance's configured base URL, so it is classified against the instance
// organization list. An ID match is authoritative, and a name match without the
// ID means the name now belongs to a different organization. It reports whether
// the state's organization verifiably no longer exists; the caller decides what
// gone means (remove on read, succeed on delete). A 404 is the route's own gone
// answer and appends nothing.
func (r *OrganizationResource) appendOrganizationContextError(ctx context.Context, diags *diag.Diagnostics, summary string, err error, status int, state *OrganizationModel) (gone bool) {
	if status == 404 {
		return true
	}
	if !logclient.IsInvalidTargetError(err) {
		diags.AddError(summary, err.Error())
		return false
	}
	list, listErr := r.client.ListOrganizations(ctx)
	if listErr != nil {
		diags.AddError(summary, fmt.Sprintf(
			"token exchange rejected the organization audience (%v) and the follow-up organization list failed: %v", exchangeCause(err), listErr))
		return false
	}
	i := slices.IndexFunc(list, func(o logclient.OrganizationRead) bool {
		return o.ID == state.ID.ValueString()
	})
	switch {
	case i >= 0 && list[i].OrganizationName == state.Name.ValueString():
		// The organization exists under its state name, so the exchange failure
		// is an audience/base-URL mismatch.
		diags.AddError(summary, fmt.Sprintf(
			"the organization exists, but the token exchange rejected its audience: %v. Check that the provider base_url matches the instance's configured frontend host.", exchangeCause(err)))
		return false
	case i >= 0:
		diags.AddError(summary, fmt.Sprintf(
			"the organization was renamed outside Terraform and is now %q. The organization API addresses organizations by name, so re-import the resource with the new name to re-sync state: terraform import logfire_organization.<address> %q",
			list[i].OrganizationName, list[i].OrganizationName))
		return false
	}
	i = slices.IndexFunc(list, func(o logclient.OrganizationRead) bool {
		return o.OrganizationName == state.Name.ValueString()
	})
	if i < 0 {
		return true
	}
	diags.AddError(summary, fmt.Sprintf(
		"the state's organization name now belongs to a different organization (id %s). Refusing to operate on it; re-import the intended organization to re-bind state.", list[i].ID))
	return false
}

func organizationReadToModel(o *logclient.OrganizationRead, m *OrganizationModel) {
	m.ID = types.StringValue(o.ID)
	m.Name = types.StringValue(o.OrganizationName)
	m.DisplayName = optionalStringToTerraform(o.OrganizationDisplayName)
	m.GithubHandle = optionalStringToTerraform(o.GithubHandle)
	m.Location = optionalStringToTerraform(o.Location)
	m.Avatar = optionalStringToTerraform(o.Avatar)
	m.Description = optionalStringToTerraform(o.Description)
	m.HasAdminPanel = types.BoolValue(o.HasAdminPanel)
	m.GatewayEnabled = types.BoolValue(o.GatewayEnabled)
	m.AIEnabled = types.BoolValue(o.AIEnabled)
	m.CreatedAt = stringOrNull(&o.CreatedAt)
	m.UpdatedAt = stringOrNull(&o.UpdatedAt)
}

func optionalStringToTerraform(in *string) types.String {
	if in == nil || strings.TrimSpace(*in) == "" {
		return types.StringNull()
	}
	return types.StringValue(*in)
}

// terraformStringPointer returns the trimmed value, or nil when it is empty,
// null, or unknown.
func terraformStringPointer(in types.String) *string {
	value := strings.TrimSpace(in.ValueString())
	if value == "" {
		return nil
	}
	return &value
}

// trimmedIfChanged returns the trimmed planned value when it differs from state,
// and nil otherwise. Computed attributes without a configured value are unknown
// in the plan and stay unchanged.
func trimmedIfChanged(plan, state types.String) *string {
	if plan.IsUnknown() || plan.IsNull() {
		return nil
	}
	desired := strings.TrimSpace(plan.ValueString())
	if desired == state.ValueString() {
		return nil
	}
	return &desired
}
