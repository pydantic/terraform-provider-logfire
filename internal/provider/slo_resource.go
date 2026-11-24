// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

type SloResource struct{ configured }

type SloModel struct {
	ID                types.String `tfsdk:"id"`
	ProjectID         types.String `tfsdk:"project_id"`
	ScopeKind         types.String `tfsdk:"scope_kind"`
	ScopeValue        types.String `tfsdk:"scope_value"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	Source            types.String `tfsdk:"source"`
	MetricAggregation types.String `tfsdk:"metric_aggregation"`
	TotalQuery        types.String `tfsdk:"total_query"`
	BadQuery          types.String `tfsdk:"bad_query"`
	Threshold         types.String `tfsdk:"threshold"`
	Comparison        types.String `tfsdk:"comparison"`
	TargetPercent     types.String `tfsdk:"target_percent"`
	RollingWindow     types.String `tfsdk:"rolling_window"`
	Environments      types.Set    `tfsdk:"environments"`
	// Alerts holds the SLO's tier alerts; see slo_alerts.go.
	Alerts types.Object `tfsdk:"alerts"`
}

func (r *SloResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_slo"
}

func (r *SloResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire SLO (Service Level Objective).\n\n~> **Experimental:** this resource is backed by a Logfire API that is not yet stable. Its schema and behavior may change in backwards-incompatible ways in future releases. SLOs also require a Logfire plan that includes them.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SLO ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project ID (UUID) used for SLO API paths.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"scope_kind": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("service"),
				MarkdownDescription: "What the SLO is anchored to: a service (`service`) or an LLM provider (`provider`). Defaults to `service`. Changing it forces a new SLO.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.OneOf("service", "provider")},
			},
			"scope_value": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The service name (`scope_kind = \"service\"`) or provider slug like `openai` (`scope_kind = \"provider\"`). Changing it forces a new SLO.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "SLO name (unique per project).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "SLO description.",
			},
			"source": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("records"),
				MarkdownDescription: "Whether the SLO ratio is computed over span events (`records`) or metric values (`metrics`). Defaults to `records`.",
				Validators:          []validator.String{stringvalidator.OneOf("records", "metrics")},
			},
			"metric_aggregation": rschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("additive"),
				MarkdownDescription: "How a `metrics` SLO aggregates its SLI: `additive` (sum of scalar values, for delta-count metrics), `gauge_fraction` (fraction of samples meeting the condition, for gauges), `counter_rate` (sum of per-series increases, for cumulative counters), or `histogram_threshold` (fraction of histogram observations past a threshold; uses `threshold` and `comparison` instead of `bad_query`, and requires `source = \"metrics\"`). Ignored when `source = \"records\"`. Defaults to `additive`.",
				Validators: []validator.String{
					stringvalidator.OneOf(
						"additive", "gauge_fraction", "counter_rate", "histogram_threshold",
					),
				},
			},
			"total_query": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "SQL boolean expression selecting all events counted by the SLO.",
			},
			"bad_query": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "SQL boolean expression selecting the bad events counted by the SLO. Required for every mode except `metric_aggregation = \"histogram_threshold\"`, which uses `threshold` and `comparison` instead.",
			},
			"threshold": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "For `metric_aggregation = \"histogram_threshold\"`: the cutoff in the metric's native unit, as a decimal string (e.g. `\"60000\"` on a `_ms` latency metric). Required for that mode, and must be omitted otherwise.",
				Validators: []validator.String{
					thresholdValidator{},
				},
			},
			"comparison": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "For `metric_aggregation = \"histogram_threshold\"`: the good side of the `threshold`. `less_than` (good is below the threshold, the latency case) or `greater_than`. Required for that mode, and must be omitted otherwise.",
				Validators:          []validator.String{stringvalidator.OneOf("less_than", "greater_than")},
			},
			"target_percent": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Target percentage as a decimal string, exclusively between 0 and 100 (e.g. `\"99.9\"`).",
				Validators: []validator.String{
					targetPercentValidator{},
				},
			},
			"rolling_window": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Rolling evaluation window as a duration string (e.g. `\"24h\"`, `\"30d\"`). Must be between 1h and 90d. The API enforces a lower effective cap: the window cannot exceed your subscription plan's maximum SLO window, nor the project's data retention for the SLO source (`records` or `metrics`) — a longer window would compute against missing data. Requests over either cap are rejected with a validation error.",
			},
			"environments": rschema.SetAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Deployment environments the SLO is scoped to. Omit to cover all environments.",
			},
			"alerts": sloAlertsAttribute(),
		},
	}
}

// ValidateConfig fails a misconfigured SLI mode at plan time with a
// field-scoped error instead of a 422 at apply.
func (r *SloResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m SloModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateSloSliConfig(&m)...)
}

// ModifyPlan completes the planned `alerts` from state; see sloPlanAlerts.
func (r *SloResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var config, state, plan SloModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An unknown planned target compares as changed.
	targetChanged := !decimalStringsEqual(
		plan.TargetPercent.ValueString(), state.TargetPercent.ValueString())
	alerts, diags := sloPlanAlerts(ctx, config.Alerts, plan.Alerts, state.Alerts, targetChanged)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("alerts"), alerts)...)
}

// validateSloSliConfig checks the SLI-mode field pairing on a config model.
// `histogram_threshold` is a metrics-only bucket-ratio mode that swaps
// `bad_query` for `threshold` + `comparison`; every other mode is the reverse.
// Defaults are not applied during config validation, so unset
// `metric_aggregation`/`source` are read as their schema defaults. Values that
// reference other resources (unknown) are left for apply-time.
func validateSloSliConfig(m *SloModel) diag.Diagnostics {
	if m.MetricAggregation.IsUnknown() {
		return nil
	}
	agg := "additive"
	if !m.MetricAggregation.IsNull() {
		agg = m.MetricAggregation.ValueString()
	}

	var diags diag.Diagnostics

	if agg == "histogram_threshold" {
		if !m.Source.IsUnknown() && m.Source.ValueString() != "metrics" {
			diags.AddAttributeError(
				path.Root("metric_aggregation"),
				"histogram_threshold requires source = \"metrics\"",
				"metric_aggregation = \"histogram_threshold\" is a metric SLI and requires source = \"metrics\".",
			)
		}
		if m.Threshold.IsNull() {
			diags.AddAttributeError(
				path.Root("threshold"),
				"threshold is required for histogram_threshold",
				"metric_aggregation = \"histogram_threshold\" requires threshold.",
			)
		}
		if m.Comparison.IsNull() {
			diags.AddAttributeError(
				path.Root("comparison"),
				"comparison is required for histogram_threshold",
				"metric_aggregation = \"histogram_threshold\" requires comparison.",
			)
		}
		return diags
	}

	if m.BadQuery.IsNull() {
		diags.AddAttributeError(
			path.Root("bad_query"),
			"bad_query is required",
			fmt.Sprintf("bad_query is required for metric_aggregation = %q.", agg),
		)
	}
	if !m.Threshold.IsNull() && !m.Threshold.IsUnknown() {
		diags.AddAttributeError(
			path.Root("threshold"),
			"threshold is only valid for histogram_threshold",
			"threshold is only valid with metric_aggregation = \"histogram_threshold\".",
		)
	}
	if !m.Comparison.IsNull() && !m.Comparison.IsUnknown() {
		diags.AddAttributeError(
			path.Root("comparison"),
			"comparison is only valid for histogram_threshold",
			"comparison is only valid with metric_aggregation = \"histogram_threshold\".",
		)
	}
	return diags
}

func decimalStringsEqual(a, b string) bool {
	ra, okA := new(big.Rat).SetString(strings.TrimSpace(a))
	rb, okB := new(big.Rat).SetString(strings.TrimSpace(b))
	return okA && okB && ra.Cmp(rb) == 0
}

// normalizeDecimalString strips insignificant trailing fractional zeros
// ("99.9000" → "99.9", "99.000" → "99") so imported values match the usual
// configured spelling.
func normalizeDecimalString(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

func parseSloRollingWindow(s string) (time.Duration, diag.Diagnostics) {
	d, err := parseDurationText(s)
	if err == nil && (d < time.Hour || d > 90*24*time.Hour) {
		err = fmt.Errorf("rolling_window must be between 1h and 90d, got %q", s)
	}
	if err != nil {
		return 0, diag.Diagnostics{diag.NewErrorDiagnostic("Invalid rolling_window", err.Error())}
	}
	return d, nil
}

// windowMatches reports whether a configured window string denotes duration d.
func windowMatches(s types.String, d time.Duration) bool {
	prev, err := parseDurationText(s.ValueString())
	return err == nil && prev == d
}

func sloModelToCreate(ctx context.Context, m *SloModel) (logclient.SloCreate, diag.Diagnostics) {
	window, diags := parseSloRollingWindow(m.RollingWindow.ValueString())
	if diags.HasError() {
		return logclient.SloCreate{}, diags
	}
	in := logclient.SloCreate{
		ScopeKind:            m.ScopeKind.ValueString(),
		ScopeValue:           m.ScopeValue.ValueString(),
		Name:                 m.Name.ValueString(),
		TotalQuery:           m.TotalQuery.ValueString(),
		TargetPercent:        m.TargetPercent.ValueString(),
		RollingWindowSeconds: int64(window / time.Second),
		BadQuery:             m.BadQuery.ValueStringPointer(),
		Threshold:            m.Threshold.ValueStringPointer(),
		Comparison:           m.Comparison.ValueStringPointer(),
		Description:          m.Description.ValueStringPointer(),
		Source:               m.Source.ValueStringPointer(),
		MetricAggregation:    m.MetricAggregation.ValueStringPointer(),
	}
	if !m.Environments.IsNull() {
		diags := m.Environments.ElementsAs(ctx, &in.Environments, false)
		if diags.HasError() {
			return logclient.SloCreate{}, diags
		}
	}
	in.Alerts, diags = sloAlertsDelivery(ctx, m.Alerts, nil)
	if diags.HasError() {
		return logclient.SloCreate{}, diags
	}
	return in, nil
}

// changedString returns the planned value only when it differs from state, so
// unchanged fields stay out of the PATCH (the API treats the presence of any
// SLI-shape field as a predicate change and resets the SLO's cached status).
func changedString(plan, state types.String) *string {
	if plan.IsNull() || plan.Equal(state) {
		return nil
	}
	return plan.ValueStringPointer()
}

// nullableStringUpdate builds the tri-state `**string` for a nullable field:
// nil (omit) when the plan value is unchanged, an explicit null when it is
// cleared, and the value when it changes. equal compares two set values.
func nullableStringUpdate(plan, state types.String, equal func(a, b string) bool) **string {
	if plan.IsNull() {
		if state.IsNull() {
			return nil
		}
		var null *string
		return &null
	}
	v := plan.ValueString()
	if !state.IsNull() && equal(state.ValueString(), v) {
		return nil
	}
	value := &v
	return &value
}

func sloModelToUpdate(ctx context.Context, plan, state *SloModel) (logclient.SloUpdate, diag.Diagnostics) {
	exact := func(a, b string) bool { return a == b }
	payload := logclient.SloUpdate{
		Name:              changedString(plan.Name, state.Name),
		Description:       changedString(plan.Description, state.Description),
		Source:            changedString(plan.Source, state.Source),
		MetricAggregation: changedString(plan.MetricAggregation, state.MetricAggregation),
		TotalQuery:        changedString(plan.TotalQuery, state.TotalQuery),
		BadQuery:          changedString(plan.BadQuery, state.BadQuery),
		Threshold: nullableStringUpdate(
			plan.Threshold, state.Threshold, decimalStringsEqual),
		Comparison: nullableStringUpdate(plan.Comparison, state.Comparison, exact),
	}
	// Clearing the description sends an empty string.
	if plan.Description.IsNull() && !state.Description.IsNull() {
		payload.Description = new(string)
	}
	if !decimalStringsEqual(plan.TargetPercent.ValueString(), state.TargetPercent.ValueString()) {
		payload.TargetPercent = plan.TargetPercent.ValueStringPointer()
	}

	window, diags := parseSloRollingWindow(plan.RollingWindow.ValueString())
	if diags.HasError() {
		return logclient.SloUpdate{}, diags
	}
	if !windowMatches(state.RollingWindow, window) {
		seconds := int64(window / time.Second)
		payload.RollingWindowSeconds = &seconds
	}

	switch {
	case plan.Environments.IsNull() && !state.Environments.IsNull():
		payload.Environments = &[]string{}
	case !plan.Environments.IsNull() && !plan.Environments.Equal(state.Environments):
		payload.Environments = &[]string{}
		diags := plan.Environments.ElementsAs(ctx, payload.Environments, false)
		if diags.HasError() {
			return logclient.SloUpdate{}, diags
		}
	}

	// A tier is sent when its planned channels differ from state. A tier that
	// is not configured is planned as its state value, or unknown, so it is
	// never sent.
	stateAlerts, _, d := sloAlertsFromObject(ctx, state.Alerts)
	diags.Append(d...)
	alerts, d := sloAlertsDelivery(
		ctx, plan.Alerts, func(tier string, planned types.Set) bool {
			current, _, d := sloTierFromObject(ctx, *stateAlerts.tier(tier))
			diags.Append(d...)
			return !planned.Equal(current.ChannelAssignments)
		},
	)
	diags.Append(d...)
	if diags.HasError() {
		return logclient.SloUpdate{}, diags
	}
	payload.Alerts = alerts
	return payload, nil
}

func sloReadToModel(ctx context.Context, s *logclient.SloRead, m *SloModel) diag.Diagnostics {
	m.ID = types.StringValue(s.ID)
	if s.ProjectID != "" {
		m.ProjectID = types.StringValue(s.ProjectID)
	}
	m.ScopeKind = types.StringValue(s.ScopeKind)
	m.ScopeValue = types.StringValue(s.ScopeValue)
	m.Name = types.StringValue(s.Name)
	if s.Description == nil || (*s.Description == "" && m.Description.IsNull()) {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*s.Description)
	}
	m.Source = types.StringValue(s.Source)
	m.MetricAggregation = types.StringValue(s.MetricAggregation)
	m.TotalQuery = types.StringValue(s.TotalQuery)

	// `histogram_threshold` SLOs don't use bad_query (they use
	// threshold/comparison), so keep it absent regardless of what the API
	// echoes: a freshly created one returns "", but a row switched into the
	// mode in place may retain its prior predicate. For every other mode an
	// empty bad_query maps back to an absent attribute so config and state
	// agree.
	switch {
	case s.MetricAggregation == "histogram_threshold":
		m.BadQuery = types.StringNull()
	case s.BadQuery == "" && m.BadQuery.IsNull():
		m.BadQuery = types.StringNull()
	default:
		m.BadQuery = types.StringValue(s.BadQuery)
	}

	// threshold and target_percent are decimals; keep the configured spelling
	// when it denotes the same number. The API normalizes the target to a fixed
	// number of decimal places.
	switch {
	case s.Threshold == nil:
		m.Threshold = types.StringNull()
	case !decimalStringsEqual(m.Threshold.ValueString(), *s.Threshold):
		m.Threshold = types.StringValue(normalizeDecimalString(*s.Threshold))
	}
	m.Comparison = types.StringPointerValue(s.Comparison)
	if !decimalStringsEqual(m.TargetPercent.ValueString(), s.TargetPercent) {
		m.TargetPercent = types.StringValue(normalizeDecimalString(s.TargetPercent))
	}

	// The API returns the window as an ISO-8601 duration. Keep the configured
	// spelling when it denotes the same duration, and render whole days as
	// "Nd" (SLO windows are commonly day-sized).
	d, err := iso8601ToDuration(s.RollingWindow)
	switch {
	case err != nil:
		m.RollingWindow = types.StringValue("")
	case !windowMatches(m.RollingWindow, d):
		compact := durationCompact(d)
		if d > 0 && d%(24*time.Hour) == 0 {
			compact = fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
		}
		m.RollingWindow = types.StringValue(compact)
	}

	if len(s.Environments) == 0 && m.Environments.IsNull() {
		m.Environments = types.SetNull(types.StringType)
	} else {
		var diags diag.Diagnostics
		m.Environments, diags = types.SetValueFrom(ctx, types.StringType, s.Environments)
		if diags.HasError() {
			return diags
		}
	}

	var diags diag.Diagnostics
	m.Alerts, diags = sloAlertsToObject(ctx, s.Alerts, m.Alerts)
	return diags
}

func (r *SloResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SloModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, diags := sloModelToCreate(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := plan.ProjectID.ValueString()
	out, err := r.client.CreateSlo(ctx, projectID, in)
	if err != nil {
		resp.Diagnostics.AddError("Create SLO failed", err.Error())
		return
	}

	state := plan
	resp.Diagnostics.Append(sloReadToModel(ctx, out, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The SLO exists, so its state is saved even when its channels were not
	// applied. With the error, Terraform marks it tainted and replaces it on
	// the next apply.
	if diags := sloDeliveryApplied(in.Alerts, out.Alerts); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		if out.Alerts == nil {
			state.Alerts = types.ObjectNull(sloAlertsAttrTypes)
		}
	}

	tflog.Trace(ctx, "created slo", map[string]any{
		"id":         state.ID.ValueString(),
		"project_id": projectID,
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SloResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SloModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Crossplane observes before create with an absent ID.
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	projectID := state.ProjectID.ValueString()
	out, status, err := r.client.GetSlo(ctx, projectID, state.ID.ValueString())
	if err != nil {
		if status == 404 || projectMissing(ctx, r.client, projectID) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read SLO failed", err.Error())
		return
	}

	resp.Diagnostics.Append(sloReadToModel(ctx, out, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SloResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SloModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError(
			"Missing ID", "Cannot update SLO because the current state has no ID.")
		return
	}

	payload, diags := sloModelToUpdate(ctx, &plan, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.UpdateSlo(
		ctx, state.ProjectID.ValueString(), state.ID.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update SLO failed", err.Error())
		return
	}

	newState := plan
	resp.Diagnostics.Append(sloReadToModel(ctx, out, &newState)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Without `alerts` in the response, keep the prior channels in state so
	// the next plan sends the configured channels again.
	if diags := sloDeliveryApplied(payload.Alerts, out.Alerts); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		if out.Alerts == nil {
			newState.Alerts = state.Alerts
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *SloResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SloModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ProjectID.ValueString()
	err := r.client.DeleteSlo(ctx, projectID, state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) &&
		!projectMissing(ctx, r.client, projectID) {
		resp.Diagnostics.AddError("Delete SLO failed", err.Error())
	}
}

func (r *SloResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	rawID := strings.TrimSpace(req.ID)
	if rawID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use either "project_id/slo_id" or "project_name/slo_name" (also accepts "," or "|").`,
		)
		return
	}

	parts, err := splitImportParts(rawID, 2)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "project_id/slo_id" or "project_name/slo_name" (also accepts "project,slo" or "project|slo").`,
		)
		return
	}
	projectKey, sloKey := parts[0], parts[1]

	projectID, projectName, err := findProjectByNameOrID(ctx, r.client, projectKey)
	if err != nil {
		resp.Diagnostics.AddError("Import SLO failed", err.Error())
		return
	}

	slos, err := r.client.ListSlos(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Import SLO failed", fmt.Sprintf("listing SLOs for project %q: %v", projectID, err))
		return
	}

	var match *logclient.SloRead
	for i := range slos {
		s := &slos[i]
		if s.ID == sloKey {
			match = s
			break
		}
		if s.Name == sloKey && match == nil {
			match = s
		}
	}
	if match == nil {
		resp.Diagnostics.AddError(
			"Import SLO failed",
			fmt.Sprintf("SLO %q not found in project %q", sloKey, projectName))
		return
	}

	state := SloModel{ProjectID: types.StringValue(projectID)}
	resp.Diagnostics.Append(sloReadToModel(ctx, match, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
