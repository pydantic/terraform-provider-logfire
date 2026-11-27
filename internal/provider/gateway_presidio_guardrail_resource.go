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

// GatewayPresidioGuardrailResource manages a Presidio entity guardrail through the public API.
type GatewayPresidioGuardrailResource struct{ configured }

// GatewayPresidioGuardrailModel stores the Presidio guardrail and its targets.
type GatewayPresidioGuardrailModel struct {
	ID             types.String                  `tfsdk:"id"`
	Name           types.String                  `tfsdk:"name"`
	Description    types.String                  `tfsdk:"description"`
	ConnectionID   types.String                  `tfsdk:"connection_id"`
	OnError        types.String                  `tfsdk:"on_error"`
	EntityType     types.String                  `tfsdk:"entity_type"`
	Language       types.String                  `tfsdk:"language"`
	Operator       *GatewayPresidioOperatorModel `tfsdk:"operator"`
	ScoreThreshold types.Float64                 `tfsdk:"score_threshold"`
	Targets        []GatewayGuardrailTargetModel `tfsdk:"targets"`
	SampleTexts    types.List                    `tfsdk:"sample_texts"`
	Status         types.String                  `tfsdk:"status"`
	InactiveReason types.String                  `tfsdk:"inactive_reason"`
	CreatedAt      types.String                  `tfsdk:"created_at"`
}

// GatewayPresidioOperatorModel selects how Presidio anonymizes a detected entity.
type GatewayPresidioOperatorModel struct {
	Type     types.String `tfsdk:"type"`
	NewValue types.String `tfsdk:"new_value"`
}

func (r *GatewayPresidioGuardrailResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_presidio_guardrail"
}

// Create stores the guardrail and its targets in one request.
func (r *GatewayPresidioGuardrailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GatewayPresidioGuardrailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.write(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateGatewayPresidioGuardrail(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create Gateway Presidio guardrail failed", err.Error()+
			". A failed request may have stored the guardrail. Check the guardrail list and import it by name before retrying if the name already exists.")
		return
	}
	plan.refresh(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the guardrail, its targets, and its status.
func (r *GatewayPresidioGuardrailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayPresidioGuardrailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}
	out, err := r.client.GetGatewayPresidioGuardrail(ctx, state.ID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway Presidio guardrail failed", err.Error())
		return
	}
	state.refresh(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the guardrail and its targets with the configured state.
func (r *GatewayPresidioGuardrailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GatewayPresidioGuardrailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	in := plan.write(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.ReplaceGatewayPresidioGuardrail(ctx, state.ID.ValueString(), in)
	if err != nil {
		resp.Diagnostics.AddError("Update Gateway Presidio guardrail failed", err.Error())
		return
	}
	plan.refresh(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the guardrail and its targets.
func (r *GatewayPresidioGuardrailResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayPresidioGuardrailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || missingID(state.ID, &resp.Diagnostics) {
		return
	}
	if err := r.client.DeleteGatewayPresidioGuardrail(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete Gateway Presidio guardrail failed", err.Error())
	}
}

// ImportState imports a Presidio guardrail by its UUID or its organization-unique name.
func (r *GatewayPresidioGuardrailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importGatewayGuardrailByIDOrName(ctx, req, resp, "Gateway Presidio guardrail",
		`terraform import logfire_gateway_presidio_guardrail.email "email-addresses"`,
		func(ctx context.Context, name string) (string, error) {
			found, err := r.client.FindGatewayPresidioGuardrailByName(ctx, name)
			if err != nil {
				return "", err
			}
			return found.ID, nil
		})
}

func (m *GatewayPresidioGuardrailModel) write(ctx context.Context, config tfsdk.Config, diags *diag.Diagnostics) logclient.GatewayPresidioGuardrailWrite {
	var operator *logclient.GatewayPresidioOperator
	if m.Operator != nil {
		operator = &logclient.GatewayPresidioOperator{
			Type: m.Operator.Type.ValueString(), NewValue: m.Operator.NewValue.ValueStringPointer(),
		}
	}
	return logclient.GatewayPresidioGuardrailWrite{
		Name:           m.Name.ValueString(),
		Description:    m.Description.ValueStringPointer(),
		ConnectionID:   m.ConnectionID.ValueString(),
		OnError:        m.OnError.ValueString(),
		EntityType:     m.EntityType.ValueString(),
		Language:       m.Language.ValueString(),
		Operator:       operator,
		ScoreThreshold: m.ScoreThreshold.ValueFloat64Pointer(),
		Targets:        gatewayGuardrailTargetsToAPI(m.Targets),
		SampleTexts:    gatewayGuardrailSampleTextsToAPI(ctx, config, m.SampleTexts, diags),
	}
}

func (m *GatewayPresidioGuardrailModel) refresh(ctx context.Context, out *logclient.GatewayPresidioGuardrailRead, diags *diag.Diagnostics) {
	m.ID = types.StringValue(out.ID)
	m.Name = types.StringValue(out.Name)
	m.Description = types.StringPointerValue(out.Description)
	m.ConnectionID = types.StringValue(out.ConnectionID)
	m.OnError = types.StringValue(out.OnError)
	m.EntityType = types.StringValue(out.EntityType)
	m.Language = types.StringValue(out.Language)
	m.Operator = nil
	if out.Operator != nil {
		m.Operator = &GatewayPresidioOperatorModel{
			Type: types.StringValue(out.Operator.Type), NewValue: types.StringPointerValue(out.Operator.NewValue),
		}
	}
	m.ScoreThreshold = types.Float64PointerValue(out.ScoreThreshold)
	m.Targets = gatewayGuardrailTargetsFromAPI(out.Targets)
	m.SampleTexts = gatewayGuardrailSampleTextsFromAPI(ctx, out.SampleTexts, diags)
	m.Status = types.StringValue(out.Status)
	m.InactiveReason = types.StringPointerValue(out.InactiveReason)
	m.CreatedAt = types.StringValue(out.CreatedAt)
}
