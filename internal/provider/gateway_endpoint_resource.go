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

const defaultGatewayEndpointWeight = 1.0

type GatewayEndpointResource struct{ configured }

// GatewayEndpointModel stores the endpoint and its complete provider
// configuration.
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

func (r *GatewayEndpointResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_endpoint"
}

// Create stores the endpoint and its providers in one request.
func (r *GatewayEndpointResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
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
			". A failed request may have stored the endpoint. Check the endpoint list and import it by slug before retrying if the slug already exists.")
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *GatewayEndpointResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayEndpointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
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

// Update patches the description and replaces the providers, each only when it
// changed.
func (r *GatewayEndpointResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GatewayEndpointModel
	var planProviders, stateProviders types.Set
	providersPath := path.Root("providers")
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(
		req.Plan.GetAttribute(ctx, providersPath, &planProviders)...)
	resp.Diagnostics.Append(
		req.State.GetAttribute(ctx, providersPath, &stateProviders)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if missingID(state.ID, &resp.Diagnostics) {
		return
	}
	id := state.ID.ValueString()
	var out *logclient.GatewayEndpointRead
	var err error
	if !plan.Description.Equal(state.Description) {
		out, err = r.client.UpdateGatewayEndpoint(ctx, id,
			logclient.GatewayEndpointUpdate{
				Description: plan.Description.ValueStringPointer(),
			})
		if err != nil {
			resp.Diagnostics.AddError("Update Gateway endpoint failed",
				err.Error())
			return
		}
	}
	if !planProviders.Equal(stateProviders) {
		out, err = r.client.ReplaceGatewayEndpointProviders(ctx, id,
			plan.apiProviders())
		if err != nil {
			resp.Diagnostics.AddError("Replace Gateway endpoint providers failed",
				err.Error())
			return
		}
	}
	if out == nil {
		out, err = r.client.GetGatewayEndpoint(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Read Gateway endpoint failed", err.Error())
			return
		}
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *GatewayEndpointResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayEndpointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if missingID(state.ID, &resp.Diagnostics) {
		return
	}
	err := r.client.DeleteGatewayEndpoint(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Delete Gateway endpoint failed", err.Error())
	}
}

// ImportState imports an endpoint by its UUID or its organization-unique slug.
func (r *GatewayEndpointResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError("Missing import ID",
			"Expected the endpoint UUID or the endpoint slug. Example: terraform import logfire_gateway_endpoint.default "+
				`"default"`)
		return
	}
	if !uuidPattern.MatchString(id) {
		found, err := r.client.FindGatewayEndpointBySlug(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Import Gateway endpoint failed",
				err.Error())
			return
		}
		id = found.ID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
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

// refresh keeps an omitted field null while the API reports its default.
// Set-nested attributes cannot use schema defaults because Terraform matches
// set elements by value.
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
		// A provider that the prior model lacks gets the zero model, whose
		// fields are all null.
		configured := prior[p.ProviderID]
		// Postgres stores weights as float32, so 0.1 reads back as
		// 0.10000000149011612.
		weight, _ := strconv.ParseFloat(
			strconv.FormatFloat(p.Weight, 'g', -1, 32), 64)
		provider := GatewayEndpointProviderModel{
			ProviderID: types.StringValue(p.ProviderID),
			Priority:   types.Int64Value(p.Priority),
			Weight:     types.Float64Value(weight),
			Active:     types.BoolValue(p.Active),
		}
		if configured.Priority.IsNull() && p.Priority == 0 {
			provider.Priority = types.Int64Null()
		}
		apiWeight := float32(p.Weight)
		if configured.Weight.IsNull() {
			if apiWeight == defaultGatewayEndpointWeight {
				provider.Weight = types.Float64Null()
			}
		} else if float32(configured.Weight.ValueFloat64()) == apiWeight {
			provider.Weight = configured.Weight
		}
		if configured.Active.IsNull() && p.Active {
			provider.Active = types.BoolNull()
		}
		m.Providers = append(m.Providers, provider)
	}
}
