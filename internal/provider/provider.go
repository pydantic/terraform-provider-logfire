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

// Option customizes the provider for a tool that embeds it.
type Option func(*LogfireProvider)

// WithDashboardManager names the tool recorded as the manager of the
// dashboards the provider writes. The default is Terraform.
func WithDashboardManager(tool string) Option {
	return func(p *LogfireProvider) { p.dashboardManager = tool }
}

func New(version string, opts ...Option) func() provider.Provider {
	return func() provider.Provider {
		p := &LogfireProvider{version: version}
		for _, opt := range opts {
			opt(p)
		}
		return p
	}
}

type LogfireProvider struct {
	version          string
	dashboardManager string
}

type LogfireProviderModel struct {
	BaseUrl       types.String `tfsdk:"base_url"`
	ApiKey        types.String `tfsdk:"api_key"`
	Organization  types.String `tfsdk:"organization"`
	CustomHeaders types.Map    `tfsdk:"custom_headers"`
}

func (p *LogfireProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "logfire"
	resp.Version = p.version
}

func (p *LogfireProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
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
			"organization": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Name of the organization that resources act in. Self-hosted instances only. The provider exchanges the instance-admin `api_key` for a short-lived token for this organization. If omitted, the provider uses LOGFIRE_ORGANIZATION. If neither is set, resources act in the organization of the `api_key`. The `organization` argument of `logfire_project` overrides this value.",
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
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.BaseUrl.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("base_url"),
			"Unknown Logfire API Host",
			"The provider cannot create the Logfire API client as there is an unknown configuration value for the Logfire API endpoint. Either target apply the source of the value first, set the value statically in the configuration, or use the LOGFIRE_BASE_URL environment variable.",
		)
	}

	if config.ApiKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Unknown Logfire API token",
			"The provider cannot create the Logfire API client as there is an unknown configuration value for the Logfire API token. Either target apply the source of the value first, set the value statically in the configuration, or use the LOGFIRE_API_KEY environment variable.",
		)
	}

	if config.Organization.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("organization"),
			"Unknown Logfire organization",
			"The provider cannot create the Logfire API client as there is an unknown configuration value for organization. Set the value statically in the configuration, use the LOGFIRE_ORGANIZATION environment variable, or pass a name that Terraform knows at plan time.",
		)
	}

	if config.CustomHeaders.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("custom_headers"),
			"Unknown custom headers",
			"The provider cannot create the Logfire API client as there is an unknown configuration value for custom_headers. Either target apply the source of the value first or set the value statically in the configuration.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	baseURL := os.Getenv("LOGFIRE_BASE_URL")
	if !config.BaseUrl.IsNull() {
		baseURL = config.BaseUrl.ValueString()
	}

	apiKey := os.Getenv("LOGFIRE_API_KEY")
	if !config.ApiKey.IsNull() {
		apiKey = config.ApiKey.ValueString()
	}

	organization := os.Getenv("LOGFIRE_ORGANIZATION")
	if !config.Organization.IsNull() {
		organization = config.Organization.ValueString()
	}
	if organization != "" && !organizationNamePattern.MatchString(organization) {
		resp.Diagnostics.AddAttributeError(
			path.Root("organization"),
			"Invalid Logfire organization",
			fmt.Sprintf("The organization %q (from the provider block or LOGFIRE_ORGANIZATION) is not an organization name. An organization name %s.", organization, organizationNameRule),
		)
	}

	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Missing Logfire API Api Key",
			"The provider cannot create the Logfire API client as there is a missing or empty value for the Logfire API Api Key. Set the api_key value in the configuration or use the LOGFIRE_API_KEY environment variable. If either is already set, ensure the value is not empty.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	if baseURL == "" {
		inferred, err := inferBaseURLFromAPIKey(apiKey)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("base_url"),
				"Unable to infer Logfire API Base URL",
				"The provider cannot create the Logfire API client because base_url was not set and the api_key does not identify a supported Logfire SaaS region. "+
					fmt.Sprintf("Reason: %s. ", err)+
					"Set the base_url value in the configuration or use the LOGFIRE_BASE_URL environment variable. Self-hosted customers should always set base_url explicitly.",
			)
			return
		}
		baseURL = inferred
	}

	tflog.Debug(ctx, "logfire endpoint", map[string]any{"base_url": baseURL})
	userAgent := "terraform-provider-logfire"
	if p.version != "" {
		userAgent += "/" + p.version
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
		baseURL,
		apiKey,
		nil,
		client.WithUserAgent(userAgent),
		client.WithAdditionalHeaders(headers),
		client.WithDashboardManager(p.dashboardManager))
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	scoped := apiClient.ForOrganization(organization)
	resp.DataSourceData = scoped
	resp.ResourceData = scoped
	tflog.Info(ctx, "Logfire client configured successfully")
}

func customHeadersFromConfig(ctx context.Context, config types.Map) (http.Header, diag.Diagnostics) {
	var diags diag.Diagnostics
	headers := http.Header{}
	var values map[string]string
	diags.Append(config.ElementsAs(ctx, &values, false)...)
	if diags.HasError() {
		return headers, diags
	}

	for name, value := range values {
		headerPath := path.Root("custom_headers").AtMapKey(name)
		_, reserved := reservedCustomHeaders[strings.ToLower(name)]
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
		case reserved:
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

// Cloud API keys encode the target region as pylf_v{1,2}_{region}_...
func inferBaseURLFromAPIKey(apiKey string) (string, error) {
	parts := strings.SplitN(apiKey, "_", 5)
	switch {
	case len(parts) < 4 || parts[0] != "pylf",
		parts[1] == "v2" && len(parts) < 5:
		return "", fmt.Errorf("invalid api_key format")
	case parts[1] != "v1" && parts[1] != "v2":
		return "", fmt.Errorf("unsupported api_key format %q", parts[1])
	}

	switch region := parts[2]; region {
	case "us":
		return defaultUSBaseURL, nil
	case "eu":
		return defaultEUBaseURL, nil
	default:
		return "", fmt.Errorf("unsupported api_key region %q", region)
	}
}

func (p *LogfireProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		func() datasource.DataSource { return &PagerDutyServiceDataSource{} },
	}
}

func (p *LogfireProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource { return &AlertResource{} },
		func() resource.Resource { return &APIKeyResource{} },
		func() resource.Resource { return &ChannelResource{} },
		func() resource.Resource { return &DashboardResource{} },
		func() resource.Resource { return &FrontendApplicationResource{} },
		func() resource.Resource { return &FrontendApplicationTokenResource{} },
		func() resource.Resource { return &GatewayAPIKeyResource{} },
		func() resource.Resource { return &GatewayEndpointResource{} },
		func() resource.Resource { return &GatewayProjectSpendingLimitsResource{} },
		func() resource.Resource { return &GatewayProviderResource{} },
		func() resource.Resource { return &GatewaySpendingLimitsResource{} },
		func() resource.Resource { return &GroupMappingsResource{} },
		func() resource.Resource { return &OrganizationResource{} },
		func() resource.Resource { return &ProjectResource{} },
		NewReadTokenResource,
		func() resource.Resource { return &ScheduleResource{} },
		func() resource.Resource { return &SloResource{} },
		NewWriteTokenResource,
	}
}

// configured gives a resource the provider's API client.
type configured struct{ client *client.APIClient }

func (c *configured) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	c.client, _ = req.ProviderData.(*client.APIClient)
}
