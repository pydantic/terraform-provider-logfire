// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var (
	_ resource.Resource                = &OrganizationMemberResource{}
	_ resource.ResourceWithConfigure   = &OrganizationMemberResource{}
	_ resource.ResourceWithImportState = &OrganizationMemberResource{}
	_ resource.ResourceWithModifyPlan  = &OrganizationMemberResource{}
)

func NewOrganizationMemberResource() resource.Resource { return &OrganizationMemberResource{} }

// organizationMemberRoles are the roles that the members API accepts.
var organizationMemberRoles = []string{"admin", "member", "guest", "billing"}

var userIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type OrganizationMemberResource struct {
	client *logclient.APIClient
}

type OrganizationMemberModel struct {
	ID           types.String `tfsdk:"id"`
	UserID       types.String `tfsdk:"user_id"`
	Organization types.String `tfsdk:"organization"`
	Role         types.String `tfsdk:"role"`
	Email        types.String `tfsdk:"email"`
	Name         types.String `tfsdk:"name"`
}

// organizationValue is the organization that state shows for a resource that
// client reached: the configured name, else the organization that client acts
// in. A client of the credential's own organization shows the organization that
// the key reports for itself, which needs organization:read.
func organizationValue(ctx context.Context, configured types.String, client *logclient.APIClient) types.String {
	if organizationSet(configured) {
		return configured
	}
	if name := client.Organization(); name != "" {
		return types.StringValue(name)
	}
	if own, _ := client.OwnOrganization(ctx); own != nil {
		return types.StringValue(own.OrganizationName)
	}
	return types.StringNull()
}

// confirmOrganization fails unless the provider can name the organization that client acts in.
// A client reached through the exchange acts in the organization that it names. A client of the
// credential's own organization needs a key that can read it. Removing a member cannot be undone,
// so a member is never managed in an organization that the provider cannot confirm.
func confirmOrganization(ctx context.Context, client *logclient.APIClient) diag.Diagnostics {
	var diags diag.Diagnostics
	if client.Organization() != "" {
		return diags
	}
	own, err := client.OwnOrganization(ctx)
	switch {
	case err != nil:
		diags.AddError("Cannot read the organization of the API key", fmt.Sprintf(
			"The provider records the organization of every member, and the instance did not answer the request for the "+
				"organization of the key: %v. Nothing was changed. Run terraform apply again.", err))
	case own == nil:
		diags.AddError("Cannot confirm the organization of the API key",
			"The provider manages members only in an organization that it can confirm. This key cannot read its own organization. "+
				"Add the organization:read scope to the key, or set organization on the resource or the provider and use an "+
				"instance-admin key.")
	}
	return diags
}

func (r *OrganizationMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_member"
}

func (r *OrganizationMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the role of an existing member of an organization, by user ID. " +
			"By default that is the organization of the provider's credential, which the key must be able to read (`organization:read`). " +
			"On a self-hosted instance, an instance-admin credential acts in another organization with the `organization` argument or the provider `organization`. " +
			"The API cannot add a member, so the user must already be a member. " +
			"Creating the resource adopts the member and sets the role. Destroying it removes the user from the organization. " +
			"The provider API key requires the `organization:read_member` and `organization:write_member` scopes. " +
			"It needs a Logfire backend from 2026-06-25 (v2026-06-25.01) or newer, which reads the organization of the key and exchanges it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"user_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The ID of the user, as `id` in the `logfire_organization_members` data source. " +
					"Changing it replaces the resource, which removes the previous user from the organization.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(userIDPattern, "must be a user ID, a lowercase UUID")},
			},
			"organization": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Organization name. Defaults to the provider `organization`, else the organization of the provider credential. " +
					"Setting it acts in that organization through the instance-admin token exchange (self-hosted only). " +
					"Changing it replaces the resource, which removes the user from the old organization.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
				Validators: organizationNameValidators(),
			},
			"role": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The member's role: `admin`, `member`, `guest`, or `billing`. " +
					"A custom role that someone set in the UI is read back by its name, so the next plan shows a change back to this role.",
				Validators: []validator.String{stringvalidator.OneOf(organizationMemberRoles...)},
			},
			"email": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The member's email address, for information.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The member's name, for information.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *OrganizationMemberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan refuses plans that act in an organization the provider cannot vouch for, as for a
// project: a replacement into an organization that does not exist or is not known yet, and a
// change by a provider that now acts in another organization than the one in state. It also
// refuses a replacement for a user ID that is not known yet, because the delete cannot be undone.
func (r *OrganizationMemberResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var prior, planned, configured, userID, priorUserID types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("organization"), &prior)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("organization"), &planned)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("organization"), &configured)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("user_id"), &userID)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("user_id"), &priorUserID)...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch {
	case configured.IsNull():
		resp.Diagnostics.Append(checkUnsetOrganization(r.client, "member", prior)...)
	case planned.IsUnknown() || (organizationSet(planned) && !planned.Equal(prior)):
		resp.Diagnostics.Append(checkReplacementOrganization(ctx, r.client, "member", prior, planned)...)
	}
	if resp.Diagnostics.HasError() || userID.Equal(priorUserID) {
		return
	}
	if userID.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("user_id"), "User ID not known until apply", fmt.Sprintf(
			"The user ID of this member is not known yet. If it changes, Terraform replaces the member by removing the user from %q "+
				"first, and it cannot check before that whether the new user is a member. Apply what produces the user ID first, "+
				"for example with terraform apply -target, so that it is known at plan time.", prior.ValueString()))
		return
	}
	// The create of a replacement has no state, so with no organization set it acts in the organization of
	// the credential, which a key of another organization or an instance-admin key can make another one.
	if configured.IsNull() && r.client.Organization() == "" {
		own, _ := r.client.OwnOrganization(ctx)
		if own == nil {
			resp.Diagnostics.AddAttributeError(path.Root("user_id"), "Cannot confirm the organization of the replacement",
				"A new user ID replaces the member, and the create acts in the organization of the API key, which cannot read its "+
					"organization (it needs organization:read). Set organization on the resource.")
			return
		}
		resp.Diagnostics.Append(checkUnsetOrganization(r.client.ForOrganization(own.OrganizationName), "member", prior)...)
	}
}

func (r *OrganizationMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	api := clientForOrganization(r.client, plan.Organization)
	resp.Diagnostics.Append(confirmOrganization(ctx, api)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The record comes first, so that a failure to write it cannot follow a role change.
	resp.Diagnostics.Append(saveOrganizationContext(ctx, resp.Private, api)...)
	if resp.Diagnostics.HasError() {
		return
	}

	members, err := api.ListMembers(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Create organization member failed", err.Error())
		return
	}
	member := findMemberByID(members, plan.UserID.ValueString())
	if member == nil {
		resp.Diagnostics.AddError("Create organization member failed", fmt.Sprintf(
			"The user %s is not a member of the organization. The API cannot add a member, so the user must already be one. "+
				"A user becomes a member through SSO sign-in with a group mapping, through SCIM, or through an invitation.",
			plan.UserID.ValueString()))
		return
	}

	if member.Role != plan.Role.ValueString() {
		if err := api.UpdateMemberRole(ctx, member.ID, plan.Role.ValueString()); err != nil {
			resp.Diagnostics.AddError("Create organization member failed", logclient.MemberRequestError("setting the role", err).Error())
			return
		}
	}

	state := OrganizationMemberModel{
		ID:           types.StringValue(member.ID),
		UserID:       plan.UserID,
		Organization: organizationValue(ctx, plan.Organization, api),
		Role:         plan.Role,
		Email:        types.StringValue(member.Email),
		Name:         types.StringValue(member.Name),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	api, found, diags := clientForState(ctx, r.client, state.Organization, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	members, err := api.ListMembers(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Read organization member failed", err.Error())
		return
	}
	member := findMemberByID(members, state.UserID.ValueString())
	if member == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	if name := organizationValue(ctx, types.StringNull(), api); !name.IsNull() {
		state.Organization = name
	}
	state.Role = types.StringValue(member.Role)
	state.Email = types.StringValue(member.Email)
	state.Name = types.StringValue(member.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveOrganizationContext(ctx, resp.Private, api)...)
}

func (r *OrganizationMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state OrganizationMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	api, found, diags := clientForState(ctx, r.client, state.Organization, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Update organization member failed", organizationDeletedDetail)
		return
	}

	if err := api.UpdateMemberRole(ctx, state.UserID.ValueString(), plan.Role.ValueString()); err != nil {
		resp.Diagnostics.AddError("Update organization member failed", logclient.MemberRequestError("setting the role", err).Error())
		return
	}

	state.Role = plan.Role
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveOrganizationContext(ctx, resp.Private, api)...)
}

func (r *OrganizationMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	api, found, diags := clientForState(ctx, r.client, state.Organization, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || !found {
		// A deleted organization took its members with it.
		return
	}

	err := api.DeleteMember(ctx, state.UserID.ValueString())
	if err != nil && !logclient.IsMemberNotFoundError(err) {
		resp.Diagnostics.AddError("Delete organization member failed", logclient.MemberRequestError("removing the member", err).Error())
	}
}

// ImportState takes the user ID, or "<organization>/<user_id>" for a member of another organization.
// A user ID is a UUID and has no slash. Without an organization the member must belong to the
// organization that the provider acts in, which is the credential's own one unless the provider sets it.
func (r *OrganizationMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	organization, userID, hasOrganization := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !hasOrganization {
		organization, userID = "", organization
	}
	if !userIDPattern.MatchString(userID) || (hasOrganization && !organizationNamePattern.MatchString(organization)) {
		resp.Diagnostics.AddError("Invalid import ID format",
			`Expected a user ID, or "<organization>/<user_id>" for a member of another organization. Example:
terraform import logfire_organization_member.alice "team-a/0191a5c0-0000-7000-8000-00000000a11c"`)
		return
	}
	api := clientForOrganization(r.client, types.StringValue(organization))
	resp.Diagnostics.Append(confirmOrganization(ctx, api)...)
	if resp.Diagnostics.HasError() {
		return
	}
	members, err := api.ListMembers(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Import organization member failed", err.Error())
		return
	}
	if findMemberByID(members, userID) == nil {
		resp.Diagnostics.AddError("Import organization member failed",
			fmt.Sprintf("the organization has no member with the user ID %q.", userID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), organizationValue(ctx, types.StringNull(), api))...)
	resp.Diagnostics.Append(saveOrganizationContext(ctx, resp.Private, api)...)
}

func findMemberByID(members []logclient.OrganizationMember, id string) *logclient.OrganizationMember {
	i := slices.IndexFunc(members, func(m logclient.OrganizationMember) bool { return m.ID == id })
	if i < 0 {
		return nil
	}
	return &members[i]
}
