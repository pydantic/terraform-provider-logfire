// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var (
	_ datasource.DataSource              = &OrganizationMembersDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationMembersDataSource{}
)

func NewOrganizationMembersDataSource() datasource.DataSource {
	return &OrganizationMembersDataSource{}
}

type OrganizationMembersDataSource struct {
	client *logclient.APIClient
}

type OrganizationMembersDataSourceModel struct {
	Organization types.String                   `tfsdk:"organization"`
	Members      []OrganizationMembersItemModel `tfsdk:"members"`
}

type OrganizationMembersItemModel struct {
	ID    types.String `tfsdk:"id"`
	Email types.String `tfsdk:"email"`
	Name  types.String `tfsdk:"name"`
	Role  types.String `tfsdk:"role"`
}

func (d *OrganizationMembersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_members"
}

func (d *OrganizationMembersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the members of an organization with their roles. " +
			"By default that is the organization of the provider's credential. " +
			"On a self-hosted instance, an instance-admin credential lists another organization with the `organization` argument or the provider `organization`. " +
			"The provider API key requires the `organization:read_member` scope. " +
			"It needs a Logfire backend from 2026-06-11 (v2026-06-11.01) or newer, and from 2026-06-25 (v2026-06-25.01) to list another organization.",
		Attributes: map[string]schema.Attribute{
			"organization": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Organization name. Defaults to the provider `organization`, else the organization of the provider credential, " +
					"which the data source shows when the key can read it (`organization:read`). Setting it acts in that organization through the instance-admin token exchange (self-hosted only).",
				Validators: organizationNameValidators(),
			},
			"members": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The members, ordered by email address.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":    schema.StringAttribute{Computed: true, MarkdownDescription: "The user ID."},
						"email": schema.StringAttribute{Computed: true, MarkdownDescription: "The member's email address."},
						"name":  schema.StringAttribute{Computed: true, MarkdownDescription: "The member's name."},
						"role": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The name of the member's role. It is a custom role's name when the role is not built in.",
						},
					},
				},
			},
		},
	}
}

func (d *OrganizationMembersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *APIClient, got %T", req.ProviderData))
		return
	}
	d.client = c
}

func (d *OrganizationMembersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var organization types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("organization"), &organization)...)
	if resp.Diagnostics.HasError() {
		return
	}
	api := clientForOrganization(d.client, organization)
	members, err := api.ListMembers(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list organization members", err.Error())
		return
	}
	slices.SortFunc(members, func(a, b logclient.OrganizationMember) int {
		return strings.Compare(strings.ToLower(a.Email)+"\x00"+a.ID, strings.ToLower(b.Email)+"\x00"+b.ID)
	})

	state := OrganizationMembersDataSourceModel{Organization: organizationValue(ctx, organization, api), Members: make([]OrganizationMembersItemModel, 0, len(members))}
	for _, m := range members {
		state.Members = append(state.Members, OrganizationMembersItemModel{
			ID:    types.StringValue(m.ID),
			Email: types.StringValue(m.Email),
			Name:  types.StringValue(m.Name),
			Role:  types.StringValue(m.Role),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
