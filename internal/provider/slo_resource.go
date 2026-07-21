// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

var _ resource.Resource = &SloResource{}
var _ resource.ResourceWithConfigure = &SloResource{}
var _ resource.ResourceWithImportState = &SloResource{}

func NewSloResource() resource.Resource { return &SloResource{} }

type SloResource struct {
	client *logclient.APIClient
}

var decimalRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

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
	TargetPercent     types.String `tfsdk:"target_percent"`
	RollingWindow     types.String `tfsdk:"rolling_window"`
	Environments      types.Set    `tfsdk:"environments"`
	PageChannelIDs    types.Set    `tfsdk:"page_channel_ids"`
	TicketChannelIDs  types.Set    `tfsdk:"ticket_channel_ids"`
}

func (r *SloResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_slo"
}

func (r *SloResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire SLO (Service Level Objective).\n\n" +
			"~> **Experimental:** this resource is backed by a Logfire API that is not yet stable. " +
			"Its schema and behavior may change in backwards-incompatible ways in future releases. " +
			"SLOs also require a Logfire plan that includes them.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SLO ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project ID (UUID) used for SLO API paths.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"scope_kind": rschema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("service"),
				MarkdownDescription: "What the SLO is anchored to: a service (`service`) or an LLM provider (`provider`). " +
					"Defaults to `service`. Changing it forces a new SLO.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("service", "provider"),
				},
			},
			"scope_value": rschema.StringAttribute{
				Required: true,
				MarkdownDescription: "The service name (`scope_kind = \"service\"`) or provider slug like `openai` " +
					"(`scope_kind = \"provider\"`). Changing it forces a new SLO.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "SLO name (unique per project).",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
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
				Validators: []validator.String{
					stringvalidator.OneOf("records", "metrics"),
				},
			},
			"metric_aggregation": rschema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("additive"),
				MarkdownDescription: "How a `metrics` SLO aggregates its SLI: `additive` (sum of scalar values, for delta-count metrics), " +
					"`gauge_fraction` (fraction of samples meeting the condition, for gauges), or `counter_rate` (sum of per-series increases, " +
					"for cumulative counters). Ignored when `source = \"records\"`. Defaults to `additive`.",
				Validators: []validator.String{
					stringvalidator.OneOf("additive", "gauge_fraction", "counter_rate"),
				},
			},
			"total_query": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "SQL boolean expression selecting all events counted by the SLO.",
			},
			"bad_query": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "SQL boolean expression selecting the bad events counted by the SLO.",
			},
			"target_percent": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Target percentage as a decimal string, exclusively between 0 and 100 (e.g. `\"99.9\"`).",
				Validators: []validator.String{
					newTargetPercentValidator(),
				},
			},
			"rolling_window": rschema.StringAttribute{
				Required: true,
				MarkdownDescription: "Rolling evaluation window as a duration string (e.g. `\"24h\"`, `\"30d\"`). Must be between 1h and 90d. " +
					"The API enforces a lower effective cap: the window cannot exceed your subscription plan's maximum SLO window, " +
					"nor the project's data retention for the SLO source (`records` or `metrics`) — a longer window would compute against missing data. " +
					"Requests over either cap are rejected with a validation error.",
			},
			"environments": rschema.SetAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Deployment environments the SLO is scoped to. Omit to cover all environments.",
			},
			"page_channel_ids": rschema.SetAttribute{
				ElementType: types.StringType,
				Optional:    true,
				MarkdownDescription: "Channel IDs seeded onto the SLO's page-severity burn-rate alerts when the SLO is created. " +
					"Delivery is alert-owned after creation: changing this attribute later updates only the Terraform state, " +
					"not the existing alerts (edit the alerts' channels instead).",
			},
			"ticket_channel_ids": rschema.SetAttribute{
				ElementType: types.StringType,
				Optional:    true,
				MarkdownDescription: "Channel IDs seeded onto the SLO's ticket-severity burn-rate alert when the SLO is created. " +
					"Delivery is alert-owned after creation: changing this attribute later updates only the Terraform state, " +
					"not the existing alerts (edit the alerts' channels instead).",
			},
		},
	}
}

func (r *SloResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *APIClient, got %T", req.ProviderData))
		return
	}
	r.client = c
}

// --- Helpers ---

const (
	minSloRollingWindow = time.Hour
	maxSloRollingWindow = 90 * 24 * time.Hour
)

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

// sloDurationCompact renders whole days as "Nd" (SLO windows are commonly
// day-sized), otherwise falls back to the generic compact form.
func sloDurationCompact(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	}
	return durationCompact(d)
}

func parseSloRollingWindow(s types.String) (time.Duration, error) {
	d, err := parseDurationStr(s)
	if err != nil {
		return 0, err
	}
	if d < minSloRollingWindow || d > maxSloRollingWindow {
		return 0, fmt.Errorf("rolling_window must be between 1h and 90d, got %q", s.ValueString())
	}
	return d, nil
}

// windowMatches reports whether a configured window string denotes duration d.
func windowMatches(s types.String, d time.Duration) bool {
	if s.IsNull() || s.IsUnknown() {
		return false
	}
	prev, err := parseDurationStr(s)
	return err == nil && prev == d
}

func sloModelToCreate(ctx context.Context, m *SloModel) (logclient.SloCreate, diag.Diagnostics) {
	window, err := parseSloRollingWindow(m.RollingWindow)
	if err != nil {
		return logclient.SloCreate{}, diag.Diagnostics{diag.NewErrorDiagnostic("Invalid rolling_window", err.Error())}
	}
	in := logclient.SloCreate{
		ScopeKind:            m.ScopeKind.ValueString(),
		ScopeValue:           m.ScopeValue.ValueString(),
		Name:                 m.Name.ValueString(),
		TotalQuery:           m.TotalQuery.ValueString(),
		BadQuery:             m.BadQuery.ValueString(),
		TargetPercent:        m.TargetPercent.ValueString(),
		RollingWindowSeconds: int64(window / time.Second),
	}
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		v := m.Description.ValueString()
		in.Description = &v
	}
	if !m.Source.IsNull() && !m.Source.IsUnknown() {
		v := m.Source.ValueString()
		in.Source = &v
	}
	if !m.MetricAggregation.IsNull() && !m.MetricAggregation.IsUnknown() {
		v := m.MetricAggregation.ValueString()
		in.MetricAggregation = &v
	}
	if !m.Environments.IsNull() && !m.Environments.IsUnknown() {
		var envs []string
		if diags := m.Environments.ElementsAs(ctx, &envs, false); diags.HasError() {
			return logclient.SloCreate{}, diags
		}
		in.Environments = envs
	}
	if !m.PageChannelIDs.IsNull() && !m.PageChannelIDs.IsUnknown() {
		var ids []string
		if diags := m.PageChannelIDs.ElementsAs(ctx, &ids, false); diags.HasError() {
			return logclient.SloCreate{}, diags
		}
		in.PageChannelIDs = ids
	}
	if !m.TicketChannelIDs.IsNull() && !m.TicketChannelIDs.IsUnknown() {
		var ids []string
		if diags := m.TicketChannelIDs.ElementsAs(ctx, &ids, false); diags.HasError() {
			return logclient.SloCreate{}, diags
		}
		in.TicketChannelIDs = ids
	}
	return in, nil
}

// changedString returns the planned value only when it differs from state, so
// unchanged fields stay out of the PATCH (the API treats the presence of any
// SLI-shape field as a predicate change and resets the SLO's cached status).
func changedString(plan, state types.String) *string {
	if plan.IsNull() || plan.IsUnknown() {
		return nil
	}
	v := plan.ValueString()
	if !state.IsNull() && !state.IsUnknown() && state.ValueString() == v {
		return nil
	}
	return &v
}

func sloModelToUpdate(ctx context.Context, plan, state *SloModel) (logclient.SloUpdate, diag.Diagnostics) {
	payload := logclient.SloUpdate{
		Name:              changedString(plan.Name, state.Name),
		Source:            changedString(plan.Source, state.Source),
		MetricAggregation: changedString(plan.MetricAggregation, state.MetricAggregation),
		TotalQuery:        changedString(plan.TotalQuery, state.TotalQuery),
		BadQuery:          changedString(plan.BadQuery, state.BadQuery),
	}

	if !plan.Description.IsUnknown() {
		switch {
		case plan.Description.IsNull():
			if !state.Description.IsNull() && !state.Description.IsUnknown() {
				empty := ""
				payload.Description = &empty
			}
		default:
			payload.Description = changedString(plan.Description, state.Description)
		}
	}

	if !plan.TargetPercent.IsNull() && !plan.TargetPercent.IsUnknown() {
		v := plan.TargetPercent.ValueString()
		if state.TargetPercent.IsNull() || state.TargetPercent.IsUnknown() || !decimalStringsEqual(state.TargetPercent.ValueString(), v) {
			payload.TargetPercent = &v
		}
	}

	if !plan.RollingWindow.IsNull() && !plan.RollingWindow.IsUnknown() {
		d, err := parseSloRollingWindow(plan.RollingWindow)
		if err != nil {
			return logclient.SloUpdate{}, diag.Diagnostics{diag.NewErrorDiagnostic("Invalid rolling_window", err.Error())}
		}
		if !windowMatches(state.RollingWindow, d) {
			seconds := int64(d / time.Second)
			payload.RollingWindowSeconds = &seconds
		}
	}

	if !plan.Environments.IsUnknown() {
		switch {
		case plan.Environments.IsNull():
			if !state.Environments.IsNull() && !state.Environments.IsUnknown() {
				empty := []string{}
				payload.Environments = &empty
			}
		case !plan.Environments.Equal(state.Environments):
			var envs []string
			if diags := plan.Environments.ElementsAs(ctx, &envs, false); diags.HasError() {
				return logclient.SloUpdate{}, diags
			}
			payload.Environments = &envs
		}
	}

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
	if s.Description == nil || (*s.Description == "" && (m.Description.IsNull() || m.Description.IsUnknown())) {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*s.Description)
	}
	m.Source = types.StringValue(s.Source)
	m.MetricAggregation = types.StringValue(s.MetricAggregation)
	m.TotalQuery = types.StringValue(s.TotalQuery)
	m.BadQuery = types.StringValue(s.BadQuery)

	// The API normalizes the target to a fixed number of decimal places; keep
	// the configured spelling when it denotes the same number.
	if m.TargetPercent.IsNull() || m.TargetPercent.IsUnknown() || !decimalStringsEqual(m.TargetPercent.ValueString(), s.TargetPercent) {
		m.TargetPercent = types.StringValue(normalizeDecimalString(s.TargetPercent))
	}

	// Same for the window: the API returns an ISO-8601 duration.
	if d, err := iso8601ToDuration(s.RollingWindow); err == nil {
		if !windowMatches(m.RollingWindow, d) {
			m.RollingWindow = types.StringValue(sloDurationCompact(d))
		}
	} else {
		m.RollingWindow = types.StringValue("")
	}

	if len(s.Environments) == 0 && (m.Environments.IsNull() || m.Environments.IsUnknown()) {
		m.Environments = types.SetNull(types.StringType)
	} else {
		set, diags := types.SetValueFrom(ctx, types.StringType, s.Environments)
		if diags.HasError() {
			return diags
		}
		m.Environments = set
	}

	// The API never returns the channel seeds (delivery is alert-owned after
	// creation), so keep whatever the config/state carries. On fresh models
	// (import) the zero value has no element type; pin it to a typed null.
	if m.PageChannelIDs.ElementType(ctx) == nil {
		m.PageChannelIDs = types.SetNull(types.StringType)
	}
	if m.TicketChannelIDs.ElementType(ctx) == nil {
		m.TicketChannelIDs = types.SetNull(types.StringType)
	}
	return nil
}

// --- CRUD ---

func (r *SloResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan SloModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ProjectID.IsNull() || plan.ProjectID.IsUnknown() || plan.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "The SLO requires a project_id to construct API paths.")
		return
	}

	projectID := plan.ProjectID.ValueString()
	in, diags := sloModelToCreate(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateSlo(ctx, projectID, in)
	if err != nil {
		resp.Diagnostics.AddError("Create SLO failed", err.Error())
		return
	}

	state := plan
	if diags := sloReadToModel(ctx, out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = plan.ProjectID
	}

	tflog.Trace(ctx, "created slo", map[string]any{"id": state.ID.ValueString(), "project_id": projectID})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SloResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state SloModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot read SLO because the state is missing a project_id.")
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	projectID := state.ProjectID.ValueString()
	id := state.ID.ValueString()

	out, status, err := r.client.GetSlo(ctx, projectID, id)
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		if projectMissing(ctx, r.client, projectID) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read SLO failed", err.Error())
		return
	}

	if diags := sloReadToModel(ctx, out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = types.StringValue(projectID)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SloResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan SloModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state SloModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot update SLO because the current state has no ID.")
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot update SLO because the current state has no project_id.")
		return
	}

	projectID := state.ProjectID.ValueString()
	id := state.ID.ValueString()

	payload, diags := sloModelToUpdate(ctx, &plan, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.UpdateSlo(ctx, projectID, id, payload)
	if err != nil {
		resp.Diagnostics.AddError("Update SLO failed", err.Error())
		return
	}

	newState := plan
	newState.ProjectID = state.ProjectID
	if diags := sloReadToModel(ctx, out, &newState); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if newState.ProjectID.IsNull() || newState.ProjectID.IsUnknown() || newState.ProjectID.ValueString() == "" {
		newState.ProjectID = state.ProjectID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *SloResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state SloModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot delete SLO because the state is missing a project_id.")
		return
	}

	projectID := state.ProjectID.ValueString()
	id := state.ID.ValueString()

	if err := r.client.DeleteSlo(ctx, projectID, id); err != nil {
		if logclient.IsNotFoundError(err) {
			// Already gone, treat as successful delete
			return
		}
		if projectMissing(ctx, r.client, projectID) {
			return
		}
		resp.Diagnostics.AddError("Delete SLO failed", err.Error())
	}
}

func (r *SloResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

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

	projectKey := parts[0]
	sloKey := parts[1]

	projectID, projectName, err := findProjectByNameOrID(ctx, r.client, projectKey)
	if err != nil {
		resp.Diagnostics.AddError("Import SLO failed", err.Error())
		return
	}

	slos, err := r.client.ListSlos(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Import SLO failed", fmt.Sprintf("listing SLOs for project %q: %v", projectID, err))
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
		resp.Diagnostics.AddError("Import SLO failed", fmt.Sprintf("SLO %q not found in project %q", sloKey, projectName))
		return
	}

	var state SloModel
	state.ProjectID = types.StringValue(projectID)
	if diags := sloReadToModel(ctx, match, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = types.StringValue(projectID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
