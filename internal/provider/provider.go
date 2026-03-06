// Copyright Pydantic, Inc. 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

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

const (
	defaultUSBaseURL = "https://logfire-us.pydantic.dev"
	defaultEUBaseURL = "https://logfire-eu.pydantic.dev"
)

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
		MarkdownDescription: "Configure the Logfire API endpoint and credentials used by all resources.",
		Attributes: map[string]schema.Attribute{
			"base_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Base URL for the Logfire API. If omitted, the provider uses LOGFIRE_BASE_URL or infers the SaaS endpoint from the api_key region. Self-hosted customers should set this explicitly.",
			},
			"api_key": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Bearer token. If omitted, the LOGFIRE_API_KEY environment variable is used.",
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

	if base_url == "" {
		inferredBaseURL, err := inferBaseURLFromAPIKey(api_key)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("base_url"),
				"Unable to infer Logfire API Base URL",
				"The provider cannot create the Logfire API client because base_url was not set and the api_key does not identify a supported Logfire SaaS region. "+
					fmt.Sprintf("Reason: %s. ", err)+
					"Set the base_url value in the configuration or use the LOGFIRE_BASE_URL environment variable. "+
					"Self-hosted customers should always set base_url explicitly.",
			)
			return
		}
		base_url = inferredBaseURL
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

// Cloud API keys encode the target region as pylf_v{1,2}_{region}_...
func inferBaseURLFromAPIKey(apiKey string) (string, error) {
	region, err := apiKeyRegion(apiKey)
	if err != nil {
		return "", err
	}

	switch region {
	case "us":
		return defaultUSBaseURL, nil
	case "eu":
		return defaultEUBaseURL, nil
	default:
		return "", fmt.Errorf("unsupported api_key region %q", region)
	}
}

func apiKeyRegion(apiKey string) (string, error) {
	parts := strings.SplitN(apiKey, "_", 5)
	if len(parts) < 4 || parts[0] != "pylf" {
		return "", fmt.Errorf("invalid api_key format")
	}

	switch parts[1] {
	case "v1":
		return parts[2], nil
	case "v2":
		if len(parts) < 5 {
			return "", fmt.Errorf("invalid api_key format")
		}
		return parts[2], nil
	default:
		return "", fmt.Errorf("unsupported api_key format %q", parts[1])
	}
}

func (p *LogfireProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func (p *LogfireProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAlertResource,
		NewChannelResource,
		NewDashboardResource,
		NewOrganizationResource,
		NewProjectResource,
		NewReadTokenResource,
		NewWriteTokenResource,
	}
}
