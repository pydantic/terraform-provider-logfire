// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// GatewayPrebuiltGuardrailResource manages the configuration of one guardrail template.
type GatewayPrebuiltGuardrailResource struct{ configured }

// GatewayPrebuiltGuardrailModel stores the targets of a prebuilt guardrail.
type GatewayPrebuiltGuardrailModel struct {
	ID             types.String                  `tfsdk:"id"`
	TemplateSlug   types.String                  `tfsdk:"template_slug"`
	Targets        []GatewayGuardrailTargetModel `tfsdk:"targets"`
	SampleMessages types.List                    `tfsdk:"sample_messages"`
	Name           types.String                  `tfsdk:"name"`
	Description    types.String                  `tfsdk:"description"`
	Category       types.String                  `tfsdk:"category"`
}

func (r *GatewayPrebuiltGuardrailResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_prebuilt_guardrail"
}

// Create saves the template configuration with an idempotent PUT.
func (r *GatewayPrebuiltGuardrailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GatewayPrebuiltGuardrailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.save(ctx, req.Config, &plan, "Create")...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the targets. A template that is not configured is removed from state.
func (r *GatewayPrebuiltGuardrailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayPrebuiltGuardrailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}
	out, err := r.client.GetGatewayPrebuiltGuardrail(ctx, state.ID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway prebuilt guardrail failed", err.Error())
		return
	}
	resp.Diagnostics.Append(state.refresh(ctx, out)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the targets, and the samples when the configuration sets them.
func (r *GatewayPrebuiltGuardrailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GatewayPrebuiltGuardrailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(r.save(ctx, req.Config, &plan, "Update")...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the template configuration.
func (r *GatewayPrebuiltGuardrailResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayPrebuiltGuardrailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	err := r.client.DeleteGatewayPrebuiltGuardrail(ctx, state.ID.ValueString())
	// A 404 means that the template left the catalog, so nothing is left to delete.
	if err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Delete Gateway prebuilt guardrail failed", err.Error())
	}
}

// ImportState imports a prebuilt guardrail by its template slug.
func (r *GatewayPrebuiltGuardrailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	slug := strings.TrimSpace(req.ID)
	if slug == "" {
		resp.Diagnostics.AddError("Missing import ID", `Expected the template slug. Example: terraform import logfire_gateway_prebuilt_guardrail.email_addresses "pii-email"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), slug)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("template_slug"), slug)...)
}

func (r *GatewayPrebuiltGuardrailResource) save(ctx context.Context, config tfsdk.Config, plan *GatewayPrebuiltGuardrailModel, verb string) (diags diag.Diagnostics) {
	samples := gatewayGuardrailSampleMessagesToAPI(ctx, config, plan.SampleMessages, &diags)
	if diags.HasError() {
		return diags
	}
	out, err := r.client.SaveGatewayPrebuiltGuardrail(ctx, plan.TemplateSlug.ValueString(),
		logclient.GatewayPrebuiltGuardrailSave{Targets: gatewayGuardrailTargetsToAPI(plan.Targets), SampleMessages: samples})
	if err != nil {
		diags.AddError(verb+" Gateway prebuilt guardrail failed", err.Error())
		return diags
	}
	diags.Append(plan.refresh(ctx, out)...)
	return diags
}

func (m *GatewayPrebuiltGuardrailModel) refresh(ctx context.Context, out *logclient.GatewayPrebuiltGuardrailRead) (diags diag.Diagnostics) {
	m.ID = types.StringValue(out.TemplateSlug)
	m.TemplateSlug = types.StringValue(out.TemplateSlug)
	m.Name = types.StringValue(out.Name)
	m.Description = types.StringValue(out.Description)
	m.Category = types.StringValue(out.Category)
	m.Targets = gatewayGuardrailTargetsFromAPI(out.Targets)
	m.SampleMessages = gatewayGuardrailSampleMessagesFromAPI(ctx, out.SampleMessages, &diags)
	return diags
}
