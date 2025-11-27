// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/pydantic/terraform-provider-logfire/internal/client"
)

// Interface assertions.
var _ provider.Provider = &LogfireProvider{}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &LogfireProvider{version: version} }
}

type LogfireProvider struct{ version string }

type LogfireProviderModel struct {
	BaseUrl types.String `tfsdk:"base_url"`
	ApiKey  types.String `tfsdk:"api_key"`
}

func (p *LogfireProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "logfire"
	resp.Version = p.version
}

func (p *LogfireProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"base_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Base URL for Logfire API. If omitted, the LOGFIRE_BASE_URL environment variable is used.",
			},
			"api_key": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Bearer token.",
			},
		},
	}
}

func (p *LogfireProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	tflog.Info(ctx, "Configuring Logfire client")
	var config LogfireProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.BaseUrl.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("base_url"),
			"Unknown Logfire API Host",
			"The provider cannot create the Logfire API client as there is an unknown configuration value for the Logfire API endpoint. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the LOGFIRE_BASE_URL environment variable.",
		)
	}

	if config.ApiKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Unknown Logfire API token",
			"The provider cannot create the Logfire API client as there is an unknown configuration value for the Logfire API token. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the LOGFIRE_API_KEY environment variable.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	base_url := os.Getenv("LOGFIRE_BASE_URL")
	api_key := os.Getenv("LOGFIRE_API_KEY")

	if !config.BaseUrl.IsNull() {
		base_url = config.BaseUrl.ValueString()
	}

	if !config.ApiKey.IsNull() {
		api_key = config.ApiKey.ValueString()
	}

	if base_url == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("base_url"),
			"Missing Logfire API Base URL",
			"The provider cannot create the Logfire API client as there is a missing or empty value for the Logfire API base url. "+
				"Set the base_url value in the configuration or use the LOGFIRE_BASE_URL environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if api_key == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Missing Logfire API Api Key",
			"The provider cannot create the Logfire API client as there is a missing or empty value for the Logfire API Api Key. "+
				"Set the api_key value in the configuration or use the LOGFIRE_API_KEY environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "logfire endpoint", map[string]interface{}{"base_url": base_url})
	ua := "terraform-provider-logfire"
	if p.version != "" {
		ua = fmt.Sprintf("%s/%s", ua, p.version)
	}

	headers := http.Header{}
	if p.version != "" {
		headers.Set("X-Terraform-Provider-Version", p.version)
	}

	apiClient, err := client.NewAPIClient(
		base_url,
		api_key,
		nil,
		client.WithUserAgent(ua),
		client.WithAdditionalHeaders(headers))

	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	resp.DataSourceData = apiClient
	resp.ResourceData = apiClient
	tflog.Info(ctx, "Logfire client configured successfully")
}

func (p *LogfireProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func (p *LogfireProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAlertResource,
		NewChannelResource,
		NewDashboardResource,
		NewProjectResource,
		NewReadTokenResource,
		NewWriteTokenResource,
	}
}
