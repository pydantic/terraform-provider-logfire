// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

type GatewayOptimizationRuleResource struct{ configured }

// GatewayOptimizationRuleModel stores the rule and its complete set of targets.
type GatewayOptimizationRuleModel struct {
	ID             types.String                     `tfsdk:"id"`
	Name           types.String                     `tfsdk:"name"`
	Description    types.String                     `tfsdk:"description"`
	Category       types.String                     `tfsdk:"category"`
	Instruction    types.String                     `tfsdk:"instruction"`
	MessageRole    types.String                     `tfsdk:"message_role"`
	TriggerPattern types.String                     `tfsdk:"trigger_pattern"`
	Priority       types.Int64                      `tfsdk:"priority"`
	FailureMode    types.String                     `tfsdk:"failure_mode"`
	Action         types.String                     `tfsdk:"action"`
	Enabled        types.Bool                       `tfsdk:"enabled"`
	Targets        []GatewayOptimizationTargetModel `tfsdk:"targets"`
	Source         types.String                     `tfsdk:"source"`
	TemplateSlug   types.String                     `tfsdk:"template_slug"`
	CreatedAt      types.String                     `tfsdk:"created_at"`
}

// GatewayOptimizationTargetModel selects the requests on one endpoint that the rule applies to.
type GatewayOptimizationTargetModel struct {
	EndpointSlug        types.String `tfsdk:"endpoint_slug"`
	ModelPattern        types.String `tfsdk:"model_pattern"`
	AgentName           types.String `tfsdk:"agent_name"`
	SystemPromptPattern types.String `tfsdk:"system_prompt_pattern"`
}

// Metadata returns the Terraform resource type.
func (r *GatewayOptimizationRuleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_optimization_rule"
}

// Create stores the rule and its targets in one request.
func (r *GatewayOptimizationRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GatewayOptimizationRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateGatewayOptimizationRule(ctx, plan.apiWrite())
	if err != nil {
		if isPatternCheckUnavailable(err) {
			resp.Diagnostics.AddError("Create Gateway optimization rule failed", err.Error()+patternCheckUnavailableHint)
			return
		}
		resp.Diagnostics.AddError("Create Gateway optimization rule failed", err.Error()+
			". A failed request may have stored the rule. "+
			"Check the optimization rule list and import the rule by name before retrying if the name already exists.")
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the rule and its targets.
func (r *GatewayOptimizationRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayOptimizationRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}
	out, err := r.client.GetGatewayOptimizationRule(ctx, state.ID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read Gateway optimization rule failed", err.Error())
		return
	}
	state.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the rule with the complete planned state, including its targets.
func (r *GatewayOptimizationRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GatewayOptimizationRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if missingID(state.ID, &resp.Diagnostics) {
		return
	}
	out, err := r.client.ReplaceGatewayOptimizationRule(ctx, state.ID.ValueString(), plan.apiWrite())
	if err != nil {
		hint := ""
		if isPatternCheckUnavailable(err) {
			hint = patternCheckUnavailableHint
		}
		resp.Diagnostics.AddError("Update Gateway optimization rule failed", err.Error()+hint)
		return
	}
	plan.refresh(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the rule and its targets.
func (r *GatewayOptimizationRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayOptimizationRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if missingID(state.ID, &resp.Diagnostics) {
		return
	}
	if err := r.client.DeleteGatewayOptimizationRule(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete Gateway optimization rule failed", err.Error())
	}
}

// catalogRuleFixedAttributes are the arguments that Logfire refuses to change on a catalog rule.
var catalogRuleFixedAttributes = []string{
	"name", "description", "category", "instruction", "message_role", "trigger_pattern", "priority",
	"failure_mode", "action",
}

// ModifyPlan fails the plan when it changes an argument of a catalog rule other than `enabled` and
// `targets`. Logfire refuses such a change with a 409 at apply.
func (r *GatewayOptimizationRuleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var source types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("source"), &source)...)
	if resp.Diagnostics.HasError() || source.ValueString() != "builtin" {
		return
	}
	var changed []string
	for _, name := range catalogRuleFixedAttributes {
		var planned, stored attr.Value
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(name), &planned)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &stored)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !planned.IsUnknown() && !planned.Equal(stored) {
			changed = append(changed, name)
		}
	}
	if len(changed) > 0 {
		resp.Diagnostics.AddError("Catalog optimization rule cannot change",
			"This rule comes from the Logfire optimization catalog, so Logfire allows only enabled and targets "+
				"to change. The configuration changes: "+strings.Join(changed, ", ")+". "+
				"Set each of them to the value that Logfire stores. terraform state show lists the stored values.")
	}
}

const patternCheckUnavailableHint = ". The Gateway could not check a pattern, so Logfire did not save the rule. Apply again later."

// isPatternCheckUnavailable reports the 503 that a rule write returns when the Gateway cannot check a pattern.
func isPatternCheckUnavailable(err error) bool {
	var apiErr *logclient.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusServiceUnavailable
}

// ImportState imports a rule by its UUID or its organization-unique name.
func (r *GatewayOptimizationRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError("Missing import ID",
			"Expected the rule UUID or the rule name. Example: terraform import logfire_gateway_optimization_rule.concise "+
				`"Concise answers"`)
		return
	}
	if !uuidPattern.MatchString(id) {
		found, err := r.client.FindGatewayOptimizationRuleByName(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Import Gateway optimization rule failed", err.Error())
			return
		}
		id = found.ID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (m *GatewayOptimizationRuleModel) apiWrite() logclient.GatewayOptimizationRuleWrite {
	targets := make([]logclient.GatewayOptimizationTarget, 0, len(m.Targets))
	for _, t := range m.Targets {
		targets = append(targets, logclient.GatewayOptimizationTarget{
			EndpointSlug:        t.EndpointSlug.ValueString(),
			ModelPattern:        t.ModelPattern.ValueStringPointer(),
			AgentName:           t.AgentName.ValueStringPointer(),
			SystemPromptPattern: t.SystemPromptPattern.ValueStringPointer(),
		})
	}
	return logclient.GatewayOptimizationRuleWrite{
		Name:           m.Name.ValueString(),
		Description:    m.Description.ValueStringPointer(),
		Category:       m.Category.ValueString(),
		Instruction:    m.Instruction.ValueString(),
		MessageRole:    m.MessageRole.ValueString(),
		TriggerPattern: m.TriggerPattern.ValueStringPointer(),
		Priority:       m.Priority.ValueInt64(),
		FailureMode:    m.FailureMode.ValueString(),
		Action:         m.Action.ValueString(),
		Enabled:        m.Enabled.ValueBool(),
		Targets:        targets,
	}
}

func (m *GatewayOptimizationRuleModel) refresh(out *logclient.GatewayOptimizationRuleRead) {
	m.ID = types.StringValue(out.ID)
	m.Name = types.StringValue(out.Name)
	m.Description = types.StringPointerValue(out.Description)
	m.Category = types.StringValue(out.Category)
	m.Instruction = types.StringValue(out.Instruction)
	m.MessageRole = types.StringValue(out.MessageRole)
	m.TriggerPattern = types.StringPointerValue(out.TriggerPattern)
	m.Priority = types.Int64Value(out.Priority)
	m.FailureMode = types.StringValue(out.FailureMode)
	m.Action = types.StringValue(out.Action)
	m.Enabled = types.BoolValue(out.Enabled)
	m.Source = types.StringValue(out.Source)
	m.TemplateSlug = types.StringPointerValue(out.TemplateSlug)
	m.CreatedAt = types.StringValue(out.CreatedAt)
	// A non-nil empty slice keeps an empty set in state, which matches the schema default.
	m.Targets = make([]GatewayOptimizationTargetModel, 0, len(out.Targets))
	for _, t := range out.Targets {
		m.Targets = append(m.Targets, GatewayOptimizationTargetModel{
			EndpointSlug:        types.StringValue(t.EndpointSlug),
			ModelPattern:        types.StringPointerValue(t.ModelPattern),
			AgentName:           types.StringPointerValue(t.AgentName),
			SystemPromptPattern: types.StringPointerValue(t.SystemPromptPattern),
		})
	}
}
