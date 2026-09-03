// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ datasource.DataSource = &PagerDutyServiceDataSource{}
var _ datasource.DataSourceWithConfigure = &PagerDutyServiceDataSource{}

func NewPagerDutyServiceDataSource() datasource.DataSource { return &PagerDutyServiceDataSource{} }

type PagerDutyServiceDataSource struct {
	client *logclient.APIClient
}

type PagerDutyServiceDataSourceModel struct {
	AccountSubdomain   types.String `tfsdk:"account_subdomain"`
	Region             types.String `tfsdk:"region"`
	PagerDutyServiceID types.String `tfsdk:"pagerduty_service_id"`
	InstallID          types.String `tfsdk:"install_id"`
	ServiceID          types.String `tfsdk:"service_id"`
	ServiceName        types.String `tfsdk:"service_name"`
}

func (d *PagerDutyServiceDataSource) Metadata(
	ctx context.Context,
	req datasource.MetadataRequest,
	resp *datasource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_pagerduty_service"
}

func (d *PagerDutyServiceDataSource) Schema(
	ctx context.Context,
	req datasource.SchemaRequest,
	resp *datasource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Resolves a PagerDuty service approved through a Logfire connection to the IDs required " +
			"by a `pagerduty-integration` channel. PagerDuty's external service ID is resolved to Logfire's " +
			"internal installation and resource IDs. The provider API key requires the " +
			"`organization:read_channel` scope.",
		Attributes: map[string]schema.Attribute{
			"account_subdomain": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PagerDuty account subdomain, such as `acme` from `acme.pagerduty.com`.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 255),
				},
			},
			"region": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "PagerDuty account region (`us` or `eu`). Defaults to `us`.",
				Validators: []validator.String{
					stringvalidator.OneOf("us", "eu"),
				},
			},
			"pagerduty_service_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PagerDuty's service ID, such as `PABC123`.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 255),
				},
			},
			"install_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Internal Logfire installation UUID for `logfire_channel.config.install_id`.",
			},
			"service_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Internal Logfire resource UUID for `logfire_channel.config.service_id`.",
			},
			"service_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "PagerDuty service name.",
			},
		},
	}
}

func (d *PagerDutyServiceDataSource) Configure(
	ctx context.Context,
	req datasource.ConfigureRequest,
	resp *datasource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			fmt.Sprintf("expected *APIClient, got %T", req.ProviderData),
		)
		return
	}
	d.client = client
}

func (d *PagerDutyServiceDataSource) Read(
	ctx context.Context,
	req datasource.ReadRequest,
	resp *datasource.ReadResponse,
) {
	var config PagerDutyServiceDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	region := "us"
	if !config.Region.IsNull() {
		region = config.Region.ValueString()
	}

	service, err := d.client.GetPagerDutyService(
		ctx,
		config.AccountSubdomain.ValueString(),
		region,
		config.PagerDutyServiceID.ValueString(),
	)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read PagerDuty service", err.Error())
		return
	}

	state := PagerDutyServiceDataSourceModel{
		AccountSubdomain:   config.AccountSubdomain,
		Region:             types.StringValue(region),
		PagerDutyServiceID: types.StringValue(service.PagerDutyServiceID),
		InstallID:          types.StringValue(service.InstallID),
		ServiceID:          types.StringValue(service.ServiceID),
		ServiceName:        types.StringValue(service.ServiceName),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
