// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var (
	_ resource.Resource                   = &GatewayEndpointResource{}
	_ resource.ResourceWithConfigure      = &GatewayEndpointResource{}
	_ resource.ResourceWithImportState    = &GatewayEndpointResource{}
	_ resource.ResourceWithValidateConfig = &GatewayEndpointResource{}
)

// NewGatewayEndpointResource returns the organization-owned Gateway endpoint resource.
func NewGatewayEndpointResource() resource.Resource { return &GatewayEndpointResource{} }

// GatewayEndpointResource manages a Gateway endpoint and its provider routing through the public API.
type GatewayEndpointResource struct {
	client *logclient.APIClient
}

// GatewayEndpointModel stores the endpoint and its complete provider configuration.
type GatewayEndpointModel struct {
	ID          types.String                   `tfsdk:"id"`
	Slug        types.String                   `tfsdk:"slug"`
	Description types.String                   `tfsdk:"description"`
	Providers   []GatewayEndpointProviderModel `tfsdk:"providers"`
	CreatedAt   types.String                   `tfsdk:"created_at"`
}

// GatewayEndpointProviderModel assigns one Gateway provider to the endpoint.
type GatewayEndpointProviderModel struct {
	ProviderID types.String  `tfsdk:"provider_id"`
	Priority   types.Int64   `tfsdk:"priority"`
	Weight     types.Float64 `tfsdk:"weight"`
	Active     types.Bool    `tfsdk:"active"`
}

// Metadata returns the Terraform resource type.
func (r *GatewayEndpointResource) Metadata(
	ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_gateway_endpoint"
}

// Configure receives the authenticated organization client.
func (r *GatewayEndpointResource) Configure(
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

// Create stores the endpoint and its providers in one request.
func (r *GatewayEndpointResource) Create(
	ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse,
) {
	var plan GatewayEndpointModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateGatewayEndpoint(ctx, logclient.GatewayEndpointCreate{
		Slug:        plan.Slug.ValueString(),
		Description: plan.Description.ValueStringPointer(),
		Providers:   plan.apiProviders(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Create Gateway endpoint failed", err.Error()+
			". A failed request may have stored the endpoint. "+
			"Check the endpoint list and import it by slug before retrying if the slug already exists.")
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the endpoint and its providers.
func (r *GatewayEndpointResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayEndpointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetGatewayEndpoint(ctx, state.ID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway endpoint failed", err.Error())
		return
	}
	state.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update patches the description and replaces the providers, each only when it changed.
func (r *GatewayEndpointResource) Update(
	ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse,
) {
	var plan, state GatewayEndpointModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	var out *logclient.GatewayEndpointRead
	var err error
	if !plan.Description.Equal(state.Description) {
		out, err = r.client.UpdateGatewayEndpoint(ctx, id, logclient.GatewayEndpointUpdate{
			Description: plan.Description.ValueStringPointer(),
		})
		if err != nil {
			resp.Diagnostics.AddError("Update Gateway endpoint failed", err.Error())
			return
		}
	}
	if !sameGatewayEndpointProviders(plan.Providers, state.Providers) {
		out, err = r.client.ReplaceGatewayEndpointProviders(ctx, id, plan.apiProviders())
		if err != nil {
			resp.Diagnostics.AddError("Replace Gateway endpoint providers failed", err.Error())
			return
		}
	}
	if out == nil {
		if out, err = r.client.GetGatewayEndpoint(ctx, id); err != nil {
			resp.Diagnostics.AddError("Read Gateway endpoint failed", err.Error())
			return
		}
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the endpoint.
func (r *GatewayEndpointResource) Delete(
	ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse,
) {
	var state GatewayEndpointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteGatewayEndpoint(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete Gateway endpoint failed", err.Error())
	}
}

// ImportState imports an endpoint by its UUID or its organization-unique slug.
func (r *GatewayEndpointResource) ImportState(
	ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse,
) {
	rawID := strings.TrimSpace(req.ID)
	if rawID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected the endpoint UUID or the endpoint slug. Example: terraform import logfire_gateway_endpoint.default "default"`,
		)
		return
	}
	if uuidPattern.MatchString(rawID) {
		resource.ImportStatePassthroughID(ctx, path.Root("id"), resource.ImportStateRequest{ID: rawID}, resp)
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	found, err := r.client.FindGatewayEndpointBySlug(ctx, rawID)
	if err != nil {
		resp.Diagnostics.AddError("Import Gateway endpoint failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), found.ID)...)
}

func (m *GatewayEndpointModel) apiProviders() []logclient.GatewayEndpointProvider {
	providers := make([]logclient.GatewayEndpointProvider, 0, len(m.Providers))
	for _, p := range m.Providers {
		provider := logclient.GatewayEndpointProvider{
			ProviderID: p.ProviderID.ValueString(),
			Priority:   p.Priority.ValueInt64(),
			Weight:     defaultGatewayEndpointWeight,
			Active:     true,
		}
		if !p.Weight.IsNull() {
			provider.Weight = p.Weight.ValueFloat64()
		}
		if !p.Active.IsNull() {
			provider.Active = p.Active.ValueBool()
		}
		providers = append(providers, provider)
	}
	return providers
}

const defaultGatewayEndpointWeight = 1.0

// refresh keeps an omitted field null while the API reports its default. Set-nested
// attributes cannot use schema defaults because Terraform matches set elements by value.
func (m *GatewayEndpointModel) refresh(out *logclient.GatewayEndpointRead) {
	prior := map[string]GatewayEndpointProviderModel{}
	for _, p := range m.Providers {
		prior[p.ProviderID.ValueString()] = p
	}
	m.ID = types.StringValue(out.ID)
	m.Slug = types.StringValue(out.Slug)
	m.Description = types.StringPointerValue(out.Description)
	m.CreatedAt = types.StringValue(out.CreatedAt)
	m.Providers = make([]GatewayEndpointProviderModel, 0, len(out.Providers))
	for _, p := range out.Providers {
		configured, known := prior[p.ProviderID]
		// Postgres stores weights as float32, so 0.1 reads back as 0.10000000149011612.
		weight, _ := strconv.ParseFloat(strconv.FormatFloat(p.Weight, 'g', -1, 32), 64)
		provider := GatewayEndpointProviderModel{
			ProviderID: types.StringValue(p.ProviderID),
			Priority:   types.Int64Value(p.Priority),
			Weight:     types.Float64Value(weight),
			Active:     types.BoolValue(p.Active),
		}
		if (!known || configured.Priority.IsNull()) && p.Priority == 0 {
			provider.Priority = types.Int64Null()
		}
		if (!known || configured.Weight.IsNull()) && float32(p.Weight) == defaultGatewayEndpointWeight {
			provider.Weight = types.Float64Null()
		} else if known && !configured.Weight.IsNull() && float32(configured.Weight.ValueFloat64()) == float32(p.Weight) {
			provider.Weight = configured.Weight
		}
		if (!known || configured.Active.IsNull()) && p.Active {
			provider.Active = types.BoolNull()
		}
		m.Providers = append(m.Providers, provider)
	}
}

func sameGatewayEndpointProviders(a, b []GatewayEndpointProviderModel) bool {
	if len(a) != len(b) {
		return false
	}
	byID := map[string]GatewayEndpointProviderModel{}
	for _, p := range b {
		byID[p.ProviderID.ValueString()] = p
	}
	for _, p := range a {
		other, ok := byID[p.ProviderID.ValueString()]
		if !ok || !p.Priority.Equal(other.Priority) || !p.Weight.Equal(other.Weight) || !p.Active.Equal(other.Active) {
			return false
		}
	}
	return true
}
