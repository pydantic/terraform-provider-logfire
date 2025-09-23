package provider

import (
	"context"
	"os"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Interface assertions
var _ provider.Provider = &LogfireProvider{}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &LogfireProvider{version: version} }
}

type LogfireProvider struct{ version string }

type LogfireProviderModel struct {
	BaseUrl     types.String `tfsdk:"base_url"`
	ApiKey        types.String `tfsdk:"api_key"`
	Organization types.String `tfsdk:"organization"`
	Project      types.String `tfsdk:"project"`
}

func (p *LogfireProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "logfire"
	resp.Version = p.version
}

func (p *LogfireProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"base_url": schema.StringAttribute{Required: true, MarkdownDescription: "Base URL for Logfire API."},
			"api_key": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Bearer token."},
			"organization": schema.StringAttribute{Required: true, MarkdownDescription: "Organization id/slug."},
			"project": schema.StringAttribute{Required: true, MarkdownDescription: "Project id/slug."},
		},
	}
}

func (p *LogfireProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
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
					"Either target apply the source of the value first, set the value statically in the configuration, or use the LOGFIRE_api_key environment variable.",
			)
		}

		if config.Organization.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root("organization"),
				"Unknown Logfire API organization",
				"The provider cannot create the Logfire API client as there is an unknown configuration value for the Logfire API organization. "+
					"Either target apply the source of the value first, set the value statically in the configuration, or use the LOGFIRE_ORGANIZATION environment variable.",
			)
		}

		if config.Project.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root("project"),
				"Unknown Logfire API project",
				"The provider cannot create the Logfire API client as there is an unknown configuration value for the Logfire API project. "+
					"Either target apply the source of the value first, set the value statically in the configuration, or use the LOGFIRE_PROJECT environment variable.",
			)
		}

		if resp.Diagnostics.HasError() {
			return
		}

	base_url := os.Getenv("LOGFIRE_BASE_URL")
	api_key := os.Getenv("LOGFIRE_API_KEY")
	organization := os.Getenv("LOGFIRE_ORGANIZATION")
	project := os.Getenv("LOGFIRE_PROJECT")

	if !config.BaseUrl.IsNull() {
        base_url = config.BaseUrl.ValueString()
    }

	if !config.ApiKey.IsNull() {
        api_key = config.ApiKey.ValueString()
    }

	if !config.Organization.IsNull() {
        organization = config.Organization.ValueString()
    }

	if !config.Project.IsNull() {
        project = config.Project.ValueString()
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
                "Set the api_key value in the configuration or use the LOGFIRE_api_key environment variable. "+
                "If either is already set, ensure the value is not empty.",
        )
    }

    if organization == "" {
        resp.Diagnostics.AddAttributeError(
            path.Root("organization"),
            "Missing Logfire API Organization",
            "The provider cannot create the Logfire API client as there is a missing or empty value for the Logfire API Organization. "+
                "Set the organization value in the configuration or use the LOGFIRE_ORGANIZATION environment variable. "+
                "If either is already set, ensure the value is not empty.",
        )
    }

	if project == "" {
        resp.Diagnostics.AddAttributeError(
            path.Root("project"),
            "Missing Logfire API Project",
            "The provider cannot create the Logfire API client as there is a missing or empty value for the Logfire API Project. "+
                "Set the project value in the configuration or use the LOGFIRE_PROJECT environment variable. "+
                "If either is already set, ensure the value is not empty.",
        )
    }

    if resp.Diagnostics.HasError() {
        return
    }

	httpClient := http.DefaultClient
	apiClient, err := NewAPIClient(
		base_url,
		api_key,
		organization,
		project,
		httpClient)

	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	resp.DataSourceData = apiClient
	resp.ResourceData = apiClient


}

// If you don't have any data sources, just return an empty slice.
func (p *LogfireProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func (p *LogfireProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAlertResource,
	}
}