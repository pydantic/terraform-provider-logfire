// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/pydantic/terraform-provider-logfire/internal/client"
	"golang.org/x/net/http/httpguts"
)

// Interface assertions.
var _ provider.Provider = &LogfireProvider{}

const (
	defaultUSBaseURL = "https://logfire-us.pydantic.dev"
	defaultEUBaseURL = "https://logfire-eu.pydantic.dev"
)

const customHeadersDescription = "Additional HTTP headers to include on every Logfire API request. Intended for proxy, gateway, or edge authentication. Provider-managed headers cannot be overridden."

var reservedCustomHeaders = map[string]struct{}{
	"accept":                       {},
	"authorization":                {},
	"content-type":                 {},
	"user-agent":                   {},
	"x-terraform-provider-version": {},
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &LogfireProvider{version: version} }
}

type LogfireProvider struct{ version string }

type LogfireProviderModel struct {
	BaseUrl       types.String `tfsdk:"base_url"`
	ApiKey        types.String `tfsdk:"api_key"`
	CustomHeaders types.Map    `tfsdk:"custom_headers"`
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
			"custom_headers": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: customHeadersDescription,
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

	if config.CustomHeaders.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("custom_headers"),
			"Unknown custom headers",
			"The provider cannot create the Logfire API client as there is an unknown configuration value for custom_headers. "+
				"Either target apply the source of the value first or set the value statically in the configuration.",
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

	headers, headerDiags := customHeadersFromConfig(ctx, config.CustomHeaders)
	resp.Diagnostics.Append(headerDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
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

func customHeadersFromConfig(ctx context.Context, config types.Map) (http.Header, diag.Diagnostics) {
	var diags diag.Diagnostics
	headers := http.Header{}
	if config.IsNull() {
		return headers, diags
	}

	var values map[string]string
	diags.Append(config.ElementsAs(ctx, &values, false)...)
	if diags.HasError() {
		return headers, diags
	}

	for name, value := range values {
		headerPath := path.Root("custom_headers").AtMapKey(name)
		switch {
		case name == "":
			diags.AddAttributeError(
				headerPath,
				"Invalid custom header name",
				"Custom header names must not be empty.",
			)
		case !httpguts.ValidHeaderFieldName(name):
			diags.AddAttributeError(
				headerPath,
				"Invalid custom header name",
				fmt.Sprintf("%q is not a valid HTTP header name.", name),
			)
		case isReservedCustomHeader(name):
			diags.AddAttributeError(
				headerPath,
				"Reserved custom header",
				fmt.Sprintf("%q is managed by the provider and cannot be configured in custom_headers.", name),
			)
		case value == "":
			diags.AddAttributeError(
				headerPath,
				"Invalid custom header value",
				fmt.Sprintf("Custom header %q must not have an empty value.", name),
			)
		case !httpguts.ValidHeaderFieldValue(value):
			diags.AddAttributeError(
				headerPath,
				"Invalid custom header value",
				fmt.Sprintf("Custom header %q has a value that is not a valid HTTP header value.", name),
			)
		default:
			headers.Set(name, value)
		}
	}

	return headers, diags
}

func isReservedCustomHeader(name string) bool {
	_, ok := reservedCustomHeaders[strings.ToLower(name)]
	return ok
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
