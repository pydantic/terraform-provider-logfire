// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// GatewayRegexGuardrailResource manages a custom regex guardrail through the public API.
type GatewayRegexGuardrailResource struct{ configured }

// GatewayRegexGuardrailModel stores the regex guardrail and its targets.
type GatewayRegexGuardrailModel struct {
	ID             types.String                  `tfsdk:"id"`
	Name           types.String                  `tfsdk:"name"`
	Description    types.String                  `tfsdk:"description"`
	Pattern        types.String                  `tfsdk:"pattern"`
	Flags          types.String                  `tfsdk:"flags"`
	Targets        []GatewayGuardrailTargetModel `tfsdk:"targets"`
	SampleMessages types.List                    `tfsdk:"sample_messages"`
	CreatedAt      types.String                  `tfsdk:"created_at"`
}

func (r *GatewayRegexGuardrailResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_regex_guardrail"
}

// Create stores the guardrail and its targets in one request.
func (r *GatewayRegexGuardrailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GatewayRegexGuardrailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.write(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateGatewayRegexGuardrail(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create Gateway regex guardrail failed", err.Error()+
			". A failed request may have stored the guardrail. Check the guardrail list and import it by name before retrying if the name already exists.")
		return
	}
	plan.refresh(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the guardrail and its targets.
func (r *GatewayRegexGuardrailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayRegexGuardrailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}
	out, err := r.client.GetGatewayRegexGuardrail(ctx, state.ID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway regex guardrail failed", err.Error())
		return
	}
	state.refresh(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the guardrail and its targets with the configured state.
func (r *GatewayRegexGuardrailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GatewayRegexGuardrailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	in := plan.write(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.ReplaceGatewayRegexGuardrail(ctx, state.ID.ValueString(), in)
	if err != nil {
		resp.Diagnostics.AddError("Update Gateway regex guardrail failed", err.Error())
		return
	}
	plan.refresh(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the guardrail and its targets.
func (r *GatewayRegexGuardrailResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayRegexGuardrailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	if err := r.client.DeleteGatewayRegexGuardrail(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete Gateway regex guardrail failed", err.Error())
	}
}

// ImportState imports a regex guardrail by its UUID or its organization-unique name.
func (r *GatewayRegexGuardrailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importGatewayGuardrailByIDOrName(ctx, req, resp, "Gateway regex guardrail",
		`terraform import logfire_gateway_regex_guardrail.order_ids "order-ids"`,
		func(ctx context.Context, name string) (string, error) {
			found, err := r.client.FindGatewayRegexGuardrailByName(ctx, name)
			if err != nil {
				return "", err
			}
			return found.ID, nil
		})
}

func (m *GatewayRegexGuardrailModel) write(ctx context.Context, config tfsdk.Config, diags *diag.Diagnostics) logclient.GatewayRegexGuardrailWrite {
	return logclient.GatewayRegexGuardrailWrite{
		Name:           m.Name.ValueString(),
		Description:    m.Description.ValueStringPointer(),
		Pattern:        m.Pattern.ValueString(),
		Flags:          m.Flags.ValueString(),
		Targets:        gatewayGuardrailTargetsToAPI(m.Targets),
		SampleMessages: gatewayGuardrailSampleMessagesToAPI(ctx, config, m.SampleMessages, diags),
	}
}

func (m *GatewayRegexGuardrailModel) refresh(ctx context.Context, out *logclient.GatewayRegexGuardrailRead, diags *diag.Diagnostics) {
	m.ID = types.StringValue(out.ID)
	m.Name = types.StringValue(out.Name)
	m.Description = types.StringPointerValue(out.Description)
	m.Pattern = types.StringValue(out.Pattern)
	m.Flags = types.StringValue(out.Flags)
	m.Targets = gatewayGuardrailTargetsFromAPI(out.Targets)
	m.SampleMessages = gatewayGuardrailSampleMessagesFromAPI(ctx, out.SampleMessages, diags)
	m.CreatedAt = types.StringValue(out.CreatedAt)
}
