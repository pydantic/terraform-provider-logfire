// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var (
	_ resource.Resource                   = &GatewayProviderResource{}
	_ resource.ResourceWithConfigure      = &GatewayProviderResource{}
	_ resource.ResourceWithImportState    = &GatewayProviderResource{}
	_ resource.ResourceWithValidateConfig = &GatewayProviderResource{}
)

// NewGatewayProviderResource returns the organization-owned Gateway provider resource.
func NewGatewayProviderResource() resource.Resource { return &GatewayProviderResource{} }

// GatewayProviderResource manages an upstream provider through the public API.
type GatewayProviderResource struct {
	client *logclient.APIClient
}

// GatewayProviderModel stores provider configuration and the last configured credential.
type GatewayProviderModel struct {
	ID             types.String `tfsdk:"id"`
	Slug           types.String `tfsdk:"slug"`
	Vendor         types.String `tfsdk:"vendor"`
	APIKey         types.String `tfsdk:"api_key"`
	RequirePricing types.Bool   `tfsdk:"require_pricing"`
	BedrockAPI     types.String `tfsdk:"bedrock_api"`
	BedrockRegion  types.String `tfsdk:"bedrock_region"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// Metadata returns the Terraform resource type.
func (r *GatewayProviderResource) Metadata(
	ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_gateway_provider"
}

// Configure receives the authenticated organization client.
func (r *GatewayProviderResource) Configure(
	ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Expected *APIClient.")
		return
	}
	r.client = client
}

// Create stores a provider and retains its write-only credential in Terraform state.
func (r *GatewayProviderResource) Create(
	ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse,
) {
	var plan GatewayProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateGatewayProvider(ctx, logclient.GatewayProviderCreate{
		Slug: plan.Slug.ValueString(),
		Provider: logclient.GatewayProviderVendor{
			Vendor: plan.Vendor.ValueString(),
			Config: &logclient.GatewayProviderConfig{
				Credentials: &logclient.GatewayProviderCredentials{Type: "api-key", Value: plan.APIKey.ValueString()},
				API:         plan.BedrockAPI.ValueString(),
				Region:      plan.BedrockRegion.ValueString(),
			},
		},
		Pricing: logclient.GatewayProviderPricing{Required: plan.RequirePricing.ValueBool()},
	})
	if err != nil {
		resp.Diagnostics.AddError("Create Gateway provider failed", err.Error()+
			". A failed request may have stored the provider. "+
			"Check the provider list and import its UUID before retrying if the slug already exists.")
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes public configuration without overwriting the locally retained API key.
func (r *GatewayProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetGatewayProvider(ctx, state.ID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway provider failed", err.Error())
		return
	}
	state.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes pricing and sends credentials only when their configuration changes.
func (r *GatewayProviderResource) Update(
	ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse,
) {
	var plan, state GatewayProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input := logclient.GatewayProviderUpdate{
		Pricing: logclient.GatewayProviderPricing{Required: plan.RequirePricing.ValueBool()},
	}
	config := logclient.GatewayProviderConfig{}
	if !plan.APIKey.Equal(state.APIKey) {
		config.Credentials = &logclient.GatewayProviderCredentials{Type: "api-key", Value: plan.APIKey.ValueString()}
	}
	if !plan.BedrockAPI.Equal(state.BedrockAPI) {
		config.API = plan.BedrockAPI.ValueString()
	}
	if !plan.BedrockRegion.Equal(state.BedrockRegion) {
		config.Region = plan.BedrockRegion.ValueString()
	}
	if config.Credentials != nil || config.API != "" || config.Region != "" {
		input.Provider = &logclient.GatewayProviderVendor{Vendor: plan.Vendor.ValueString(), Config: &config}
	}
	out, err := r.client.UpdateGatewayProvider(ctx, state.ID.ValueString(), input)
	if err != nil {
		resp.Diagnostics.AddError("Update Gateway provider failed", err.Error()+
			". Retry the apply before revoking the previous credential; "+
			"storage may have succeeded before Gateway confirmed the refresh.")
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the provider and waits for the API to confirm the refresh.
func (r *GatewayProviderResource) Delete(
	ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse,
) {
	var state GatewayProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteGatewayProvider(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete Gateway provider failed",
			err.Error()+". Retry the destroy before considering the provider revoked.")
	}
}

// ImportState imports a provider by its UUID within the authenticated organization.
func (r *GatewayProviderResource) ImportState(
	ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse,
) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (m *GatewayProviderModel) refresh(out *logclient.GatewayProviderRead) {
	m.ID = types.StringValue(out.ID)
	m.Slug = types.StringValue(out.Slug)
	m.Vendor = types.StringValue(out.Provider.Vendor)
	m.RequirePricing = types.BoolValue(out.Pricing.Required)
	m.CreatedAt = types.StringValue(out.CreatedAt)
	m.BedrockAPI = types.StringNull()
	m.BedrockRegion = types.StringNull()
	if out.Provider.Vendor == "bedrock" && out.Provider.Config != nil {
		m.BedrockAPI = types.StringValue(out.Provider.Config.API)
		m.BedrockRegion = types.StringValue(out.Provider.Config.Region)
	}
}
