// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// GatewayGuardrailConnectionResource manages a Presidio connection through the public API.
type GatewayGuardrailConnectionResource struct{ configured }

// GatewayGuardrailConnectionModel stores the connection and the last configured bearer token.
type GatewayGuardrailConnectionModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	BaseURL        types.String `tfsdk:"base_url"`
	AuthType       types.String `tfsdk:"auth_type"`
	BearerToken    types.String `tfsdk:"bearer_token"`
	TimeoutMS      types.Int64  `tfsdk:"timeout_ms"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	GuardrailCount types.Int64  `tfsdk:"guardrail_count"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func (r *GatewayGuardrailConnectionResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_guardrail_connection"
}

// Create stores the connection. Logfire tests an enabled connection first.
func (r *GatewayGuardrailConnectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GatewayGuardrailConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateGatewayGuardrailConnection(ctx, logclient.GatewayGuardrailConnectionCreate{
		Name:      plan.Name.ValueString(),
		Provider:  "presidio",
		BaseURL:   plan.BaseURL.ValueString(),
		Auth:      plan.auth(),
		TimeoutMS: plan.TimeoutMS.ValueInt64(),
		Enabled:   plan.Enabled.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Create Gateway guardrail connection failed", err.Error()+
			". A failed request may have stored the connection. Check the connection list and import it by name before retrying if the name already exists.")
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the connection without overwriting the locally retained bearer token.
func (r *GatewayGuardrailConnectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayGuardrailConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}
	out, err := r.client.GetGatewayGuardrailConnection(ctx, state.ID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway guardrail connection failed", err.Error())
		return
	}
	state.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sends only the changed fields. It sends `auth` only when the
// authentication or the address changed, so a rename keeps the stored token.
func (r *GatewayGuardrailConnectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GatewayGuardrailConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	var patch logclient.GatewayGuardrailConnectionUpdate
	if !plan.Name.Equal(state.Name) {
		patch.Name = plan.Name.ValueStringPointer()
	}
	if !plan.BaseURL.Equal(state.BaseURL) {
		patch.BaseURL = plan.BaseURL.ValueStringPointer()
	}
	// The API requires the token with every base_url change of a bearer
	// connection, so a stored token never follows a moved address.
	if !plan.AuthType.Equal(state.AuthType) || !plan.BearerToken.Equal(state.BearerToken) || patch.BaseURL != nil {
		auth := plan.auth()
		patch.Auth = &auth
	}
	if !plan.TimeoutMS.Equal(state.TimeoutMS) {
		patch.TimeoutMS = plan.TimeoutMS.ValueInt64Pointer()
	}
	if !plan.Enabled.Equal(state.Enabled) {
		patch.Enabled = plan.Enabled.ValueBoolPointer()
	}
	id := state.ID.ValueString()
	var out *logclient.GatewayGuardrailConnectionRead
	var err error
	if patch == (logclient.GatewayGuardrailConnectionUpdate{}) {
		out, err = r.client.GetGatewayGuardrailConnection(ctx, id)
	} else {
		out, err = r.client.UpdateGatewayGuardrailConnection(ctx, id, patch)
	}
	if err != nil {
		resp.Diagnostics.AddError("Update Gateway guardrail connection failed", err.Error()+
			". Logfire keeps the stored connection and token until an update succeeds, so retry the apply before revoking the previous token.")
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the connection. Logfire refuses while a guardrail uses it.
func (r *GatewayGuardrailConnectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayGuardrailConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	err := r.client.DeleteGatewayGuardrailConnection(ctx, state.ID.ValueString())
	if logclient.IsConflictError(err) {
		resp.Diagnostics.AddError("Delete Gateway guardrail connection failed", err.Error()+
			". Delete the Presidio guardrails that use this connection, or point them at another connection, first.")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Delete Gateway guardrail connection failed", err.Error())
	}
}

// ImportState imports a connection by its UUID or its organization-unique name.
func (r *GatewayGuardrailConnectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importGatewayGuardrailByIDOrName(ctx, req, resp, "Gateway guardrail connection", `terraform import logfire_gateway_guardrail_connection.presidio "presidio"`,
		func(ctx context.Context, name string) (string, error) {
			found, err := r.client.FindGatewayGuardrailConnectionByName(ctx, name)
			if err != nil {
				return "", err
			}
			return found.ID, nil
		})
}

func (m *GatewayGuardrailConnectionModel) auth() logclient.GatewayGuardrailConnectionAuth {
	if m.AuthType.ValueString() != "bearer" {
		return logclient.GatewayGuardrailConnectionAuth{Type: "none"}
	}
	return logclient.GatewayGuardrailConnectionAuth{Type: "bearer", Token: m.BearerToken.ValueStringPointer()}
}

func (m *GatewayGuardrailConnectionModel) refresh(out *logclient.GatewayGuardrailConnectionRead) {
	m.ID = types.StringValue(out.ID)
	m.Name = types.StringValue(out.Name)
	m.BaseURL = types.StringValue(out.BaseURL)
	m.AuthType = types.StringValue(out.Auth.Type)
	m.TimeoutMS = types.Int64Value(out.TimeoutMS)
	m.Enabled = types.BoolValue(out.Enabled)
	m.GuardrailCount = types.Int64Value(out.GuardrailCount)
	m.CreatedAt = types.StringValue(out.CreatedAt)
}
