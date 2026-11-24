// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var (
	_ resource.Resource                = &GroupMappingsResource{}
	_ resource.ResourceWithConfigure   = &GroupMappingsResource{}
	_ resource.ResourceWithImportState = &GroupMappingsResource{}
)

// groupMappingsID is the fixed ID, because the instance has one group mapping.
const groupMappingsID = "instance"

const groupMappingsHelmValue = "`groupOrganizationMapping`"

func NewGroupMappingsResource() resource.Resource { return &GroupMappingsResource{} }

type GroupMappingsResource struct {
	client *logclient.APIClient
}

type GroupMappingsModel struct {
	ID               types.String        `tfsdk:"id"`
	Mapping          []groupMappingModel `tfsdk:"mapping"`
	APIMappingStatus types.String        `tfsdk:"api_mapping_status"`
}

type groupMappingModel struct {
	GroupID           types.String                        `tfsdk:"group_id"`
	OrganizationRoles []groupMappingOrganizationRoleModel `tfsdk:"organization_roles"`
}

type groupMappingOrganizationRoleModel struct {
	Organization types.String                   `tfsdk:"organization"`
	Role         types.String                   `tfsdk:"role"`
	ProjectRoles []groupMappingProjectRoleModel `tfsdk:"project_roles"`
}

type groupMappingProjectRoleModel struct {
	Project types.String `tfsdk:"project"`
	Role    types.String `tfsdk:"role"`
}

var groupMappingProjectRoleObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"project": types.StringType,
	"role":    types.StringType,
}}

func (r *GroupMappingsResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_mappings"
}

func (r *GroupMappingsResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the identity-provider group mapping of a self-hosted Logfire instance. " +
			"The mapping assigns organization and project roles to the members of identity-provider groups at their next sign-in. " +
			"The instance has one mapping, so declare this resource once. " +
			"It conflicts with the Helm value " + groupMappingsHelmValue + ", which overrides the mapping that the API stores. " +
			"The provider credential must be an API key from the admin organization with the `instance:admin` scope " +
			"(`organization:admin` is equivalent).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Always `" + groupMappingsID + "`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"api_mapping_status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "`active` when sign-in uses the stored mapping. `overridden_by_environment` when the Helm value " +
					groupMappingsHelmValue + " is set, which makes the stored mapping unused.",
			},
			"mapping": schema.ListNestedAttribute{
				Required: true,
				MarkdownDescription: "The complete mapping, in the order that the instance stores it. " +
					"An empty list stores no mapping.",
				Validators: []validator.List{uniqueGroupIDs{}},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"group_id": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "The identity-provider group ID. Each group ID appears once.",
						Validators:          nonEmpty,
					},
					"organization_roles": schema.ListNestedAttribute{
						Required:            true,
						MarkdownDescription: "The roles that members of the group get in organizations.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"organization": schema.StringAttribute{
								Required:            true,
								MarkdownDescription: "The organization name. The organization must exist.",
								Validators:          nonEmpty,
							},
							"role": schema.StringAttribute{
								Required: true,
								MarkdownDescription: "The built-in or custom organization role name. The API does not check it, " +
									"so a wrong name gives no error and sign-in skips the role.",
								Validators: nonEmpty,
							},
							"project_roles": schema.ListNestedAttribute{
								Optional: true,
								Computed: true,
								Default: listdefault.StaticValue(
									types.ListValueMust(groupMappingProjectRoleObjectType, []attr.Value{})),
								MarkdownDescription: "The roles that members of the group get in projects of this organization. " +
									"Only listed projects are managed. Omit it or use an empty list for none.",
								NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
									"project": schema.StringAttribute{
										Required:            true,
										MarkdownDescription: "The project name. The project must exist in the organization.",
										Validators:          nonEmpty,
									},
									"role": schema.StringAttribute{
										Required: true,
										MarkdownDescription: "The built-in or custom project role name. The API does not check it, " +
											"so a wrong name gives no error and sign-in skips the role.",
										Validators: nonEmpty,
									},
								}},
							},
						}},
					},
				}},
			},
		},
	}
}

func (r *GroupMappingsResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create refuses to replace a mapping that someone else stored, because the API replaces the
// whole document and a replaced mapping changes access at the next sign-in. A stored mapping
// that equals the plan is adopted, which is what an earlier create leaves when its response was lost.
func (r *GroupMappingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GroupMappingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.client.GetGroupMappings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Read group mappings failed", err.Error())
		return
	}
	if current.APIMappingStatus == logclient.GroupMappingsStatusOverriddenByEnvironment {
		appendGroupMappingsOverriddenError(&resp.Diagnostics, "Create group mappings failed")
		return
	}
	if len(current.GroupOrganizationMapping) > 0 {
		planned := groupMappingsToAPI(plan.Mapping)
		if slices.EqualFunc(current.GroupOrganizationMapping, planned, groupMappingEqual) {
			out := GroupMappingsModel{
				ID:               types.StringValue(groupMappingsID),
				Mapping:          groupMappingsFromAPI(current.GroupOrganizationMapping),
				APIMappingStatus: types.StringValue(current.APIMappingStatus),
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
			return
		}
		resp.Diagnostics.AddError("Group mappings already exist", fmt.Sprintf(
			"The instance already stores a mapping for %d group(s). Creating this resource would replace it and change "+
				"access at the next sign-in. Import the stored mapping with "+
				"`terraform import logfire_group_mappings.<name> %s`, copy it into the configuration, and apply.",
			len(current.GroupOrganizationMapping), groupMappingsID))
		return
	}
	r.apply(ctx, plan, &resp.State, &resp.Diagnostics, "Create group mappings failed")
}

func (r *GroupMappingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GroupMappingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetGroupMappings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Read group mappings failed", err.Error())
		return
	}
	if out.APIMappingStatus == logclient.GroupMappingsStatusOverriddenByEnvironment {
		resp.Diagnostics.AddWarning("Group mappings are overridden by the environment",
			"The Helm value "+groupMappingsHelmValue+" is set, so sign-in ignores the mapping that the API stores. "+
				"Terraform keeps the stored mapping in state. Remove the Helm value for this resource to take effect.")
	}
	state.ID = types.StringValue(groupMappingsID)
	state.Mapping = groupMappingsFromAPI(out.GroupOrganizationMapping)
	state.APIMappingStatus = types.StringValue(out.APIMappingStatus)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *GroupMappingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan GroupMappingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, plan, &resp.State, &resp.Diagnostics, "Update group mappings failed")
}

// Delete stores an empty mapping. The instance cannot restore the access that earlier
// sign-ins changed or removed.
func (r *GroupMappingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if _, err := r.client.SetGroupMappings(ctx, nil); err != nil {
		if errors.Is(err, logclient.ErrGroupMappingsOverridden) {
			resp.Diagnostics.AddError("Delete group mappings failed",
				"The Helm value "+groupMappingsHelmValue+" is set, so the API refuses to change the stored mapping. "+
					"Remove the Helm value and destroy again, or run `terraform state rm` to leave the stored mapping in place.")
			return
		}
		resp.Diagnostics.AddError("Delete group mappings failed", err.Error())
	}
}

func (r *GroupMappingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	rawID := strings.TrimSpace(req.ID)
	if rawID != groupMappingsID {
		resp.Diagnostics.AddError("Invalid import ID",
			`Expected "`+groupMappingsID+`". Example: terraform import logfire_group_mappings.this "`+groupMappingsID+`"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rawID)...)
}

// apply replaces the stored mapping with the planned one and stores what the instance returns.
func (r *GroupMappingsResource) apply(
	ctx context.Context, plan GroupMappingsModel, state *tfsdk.State, diags *diag.Diagnostics, summary string,
) {
	stored, err := r.client.SetGroupMappings(ctx, groupMappingsToAPI(plan.Mapping))
	if err != nil {
		var invalid *logclient.GroupMappingsInvalidError
		switch {
		case errors.Is(err, logclient.ErrGroupMappingsOverridden):
			appendGroupMappingsOverriddenError(diags, summary)
		case errors.As(err, &invalid):
			diags.AddError(summary, "The instance rejected the mapping: "+invalid.Detail)
		default:
			diags.AddError(summary, err.Error())
		}
		return
	}
	// A write that the instance accepts cannot hit the override, which answers 409 instead.
	out := GroupMappingsModel{
		ID:               types.StringValue(groupMappingsID),
		Mapping:          groupMappingsFromAPI(stored),
		APIMappingStatus: types.StringValue(logclient.GroupMappingsStatusActive),
	}
	diags.Append(state.Set(ctx, &out)...)
}

// groupMappingEqual compares two groups in the order that the instance keeps, and treats a
// missing project_roles as an empty one.
func groupMappingEqual(a, b logclient.GroupMapping) bool {
	return a.GroupID == b.GroupID && slices.EqualFunc(a.OrganizationRoles, b.OrganizationRoles,
		func(x, y logclient.GroupMappingOrganizationRole) bool {
			return x.OrganizationName == y.OrganizationName && x.Role == y.Role &&
				slices.EqualFunc(x.ProjectRoles, y.ProjectRoles, func(m, n logclient.GroupMappingProjectRole) bool { return m == n })
		})
}

func appendGroupMappingsOverriddenError(diags *diag.Diagnostics, summary string) {
	diags.AddError(summary,
		"The Helm value "+groupMappingsHelmValue+" is set, so the API refuses to change the group mapping and sign-in ignores "+
			"anything stored through it. Remove the Helm value to manage the mapping with Terraform, "+
			"or do not use this resource.")
}

func groupMappingsToAPI(in []groupMappingModel) []logclient.GroupMapping {
	out := make([]logclient.GroupMapping, 0, len(in))
	for _, group := range in {
		roles := make([]logclient.GroupMappingOrganizationRole, 0, len(group.OrganizationRoles))
		for _, role := range group.OrganizationRoles {
			projects := make([]logclient.GroupMappingProjectRole, 0, len(role.ProjectRoles))
			for _, project := range role.ProjectRoles {
				projects = append(projects, logclient.GroupMappingProjectRole{
					ProjectName: project.Project.ValueString(),
					Role:        project.Role.ValueString(),
				})
			}
			roles = append(roles, logclient.GroupMappingOrganizationRole{
				OrganizationName: role.Organization.ValueString(),
				Role:             role.Role.ValueString(),
				ProjectRoles:     projects,
			})
		}
		out = append(out, logclient.GroupMapping{GroupID: group.GroupID.ValueString(), OrganizationRoles: roles})
	}
	return out
}

// groupMappingsFromAPI never returns a nil list, so an absent project_roles and an empty one
// both become [] and the plan shows no difference.
func groupMappingsFromAPI(in []logclient.GroupMapping) []groupMappingModel {
	out := make([]groupMappingModel, 0, len(in))
	for _, group := range in {
		roles := make([]groupMappingOrganizationRoleModel, 0, len(group.OrganizationRoles))
		for _, role := range group.OrganizationRoles {
			projects := make([]groupMappingProjectRoleModel, 0, len(role.ProjectRoles))
			for _, project := range role.ProjectRoles {
				projects = append(projects, groupMappingProjectRoleModel{
					Project: types.StringValue(project.ProjectName),
					Role:    types.StringValue(project.Role),
				})
			}
			roles = append(roles, groupMappingOrganizationRoleModel{
				Organization: types.StringValue(role.OrganizationName),
				Role:         types.StringValue(role.Role),
				ProjectRoles: projects,
			})
		}
		out = append(out, groupMappingModel{GroupID: types.StringValue(group.GroupID), OrganizationRoles: roles})
	}
	return out
}

// uniqueGroupIDs rejects a repeated group_id at plan time, which the API rejects with a 400.
type uniqueGroupIDs struct{}

func (uniqueGroupIDs) Description(context.Context) string { return "each group_id appears once" }

func (v uniqueGroupIDs) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (uniqueGroupIDs) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	seen := map[string]int{}
	for i, element := range req.ConfigValue.Elements() {
		object, ok := element.(types.Object)
		if !ok || object.IsNull() || object.IsUnknown() {
			continue
		}
		id, ok := object.Attributes()["group_id"].(types.String)
		if !ok || id.IsNull() || id.IsUnknown() {
			continue
		}
		if first, repeated := seen[id.ValueString()]; repeated {
			resp.Diagnostics.AddAttributeError(req.Path.AtListIndex(i).AtName("group_id"),
				"Duplicate group_id",
				fmt.Sprintf("group_id %q already appears at index %d. The API rejects a repeated group ID.", id.ValueString(), first))
			continue
		}
		seen[id.ValueString()] = i
	}
}
