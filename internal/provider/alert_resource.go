// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ resource.Resource = &AlertResource{}
var _ resource.ResourceWithConfigure = &AlertResource{}
var _ resource.ResourceWithImportState = &AlertResource{}

func NewAlertResource() resource.Resource { return &AlertResource{} }

type AlertResource struct {
	client *logclient.APIClient
}

var alertTimeWindowConstraint = []string{"1m", "2m", "5m", "10m", "15m", "30m", "1h", "6h", "12h", "24h", "7d", "30d"}
var alertFrequencyConstraint = []string{"1m", "2m", "5m", "10m", "15m", "30m", "1h", "6h", "12h", "24h"}

type AlertModel struct {
	ID           types.String `tfsdk:"id"`
	ProjectID    types.String `tfsdk:"project_id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Query        types.String `tfsdk:"query"`
	TimeWindow   types.String `tfsdk:"time_window"`
	Frequency    types.String `tfsdk:"frequency"`
	Watermark    types.String `tfsdk:"watermark"`
	Environments types.Set    `tfsdk:"environments"`
	ChannelIDs   types.Set    `tfsdk:"channel_ids"`
	NotifyWhen   types.String `tfsdk:"notify_when"`
	Active       types.Bool   `tfsdk:"active"`
}

func (r *AlertResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert"
}

func (r *AlertResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	notification_constraint := []string{"has_matches", "has_matches_changed", "matches_changed", "starts_having_matches"}

	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire alert.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Alert ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project ID (UUID) used for alert API paths.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alert name (unique per project).",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Alert description.",
			},
			"query": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "SQL / query string used by the alert.",
			},
			"time_window": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Lookback window. Allowed values: 1m, 2m, 5m, 10m, 15m, 30m, 1h, 6h, 12h, 24h, 7d, 30d.",
				Validators: []validator.String{
					stringvalidator.OneOf(alertTimeWindowConstraint...),
				},
			},
			"frequency": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Evaluation frequency. Allowed values: 1m, 2m, 5m, 10m, 15m, 30m, 1h, 6h, 12h, 24h.",
				Validators: []validator.String{
					stringvalidator.OneOf(alertFrequencyConstraint...),
				},
			},
			"watermark": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Provider-managed watermark (lateness tolerance) sent to the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"environments": rschema.SetAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Deployment environments to scope the query to. Empty = all environments (no filter).",
			},
			"channel_ids": rschema.SetAttribute{
				ElementType:         types.StringType,
				Required:            true,
				MarkdownDescription: "Set of channel IDs to notify.",
			},
			"notify_when": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Notification rule. Must match API enum.",
				Validators: []validator.String{
					stringvalidator.OneOf(notification_constraint...),
				},
			},
			"active": rschema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the alert is active (defaults to true on creation).",
			},
		},
	}
}

func (r *AlertResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func durToISO8601(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	total := int(d.Round(time.Second).Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	b := strings.Builder{}
	b.WriteString("PT")
	if h > 0 {
		_, _ = fmt.Fprintf(&b, "%dH", h)
	}
	if m > 0 {
		_, _ = fmt.Fprintf(&b, "%dM", m)
	}
	if s > 0 || (h == 0 && m == 0) {
		_, _ = fmt.Fprintf(&b, "%dS", s)
	}
	return b.String()
}

var isoDurRe = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func iso8601ToDuration(s string) (time.Duration, error) {
	m := isoDurRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid ISO-8601 duration: %q", s)
	}
	parse := func(x string) (int, error) {
		if x == "" {
			return 0, nil
		}
		return strconv.Atoi(x)
	}
	days, err := parse(m[1])
	if err != nil {
		return 0, err
	}
	h, err := parse(m[2])
	if err != nil {
		return 0, err
	}
	mins, err := parse(m[3])
	if err != nil {
		return 0, err
	}
	sec, err := parse(m[4])
	if err != nil {
		return 0, err
	}

	return time.Duration(days)*24*time.Hour +
		time.Duration(h)*time.Hour +
		time.Duration(mins)*time.Minute +
		time.Duration(sec)*time.Second, nil
}

func durationCompact(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = -d
	}
	if d%(24*time.Hour) == 0 {
		days := int64(d / (24 * time.Hour))
		if days == 7 || days == 30 {
			return fmt.Sprintf("%dd", days)
		}
	}
	total := int64(d / time.Second)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60

	out := ""
	if h > 0 {
		out += fmt.Sprintf("%dh", h)
	}
	if m > 0 {
		out += fmt.Sprintf("%dm", m)
	}
	if s > 0 || (h == 0 && m == 0) {
		out += fmt.Sprintf("%ds", s)
	}
	return out
}

const defaultAlertWatermark = 10 * time.Second

func parseDurationStr(s types.String) (time.Duration, error) {
	raw := strings.TrimSpace(s.ValueString())
	// Interpret as Go duration string (e.g. "5m30s", "24h").
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	// Also accept day shorthand used by the provider schema (e.g. "7d", "30d").
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf("invalid duration: %q", raw)
}

func alertModelToCreate(ctx context.Context, m *AlertModel) (logclient.AlertCreate, diag.Diagnostics) {
	tw, err := parseDurationStr(m.TimeWindow)
	if err != nil {
		return logclient.AlertCreate{}, diag.Diagnostics{diag.NewErrorDiagnostic("Invalid duration", fmt.Sprintf("time_window: %v", err))}
	}
	fr, err := parseDurationStr(m.Frequency)
	if err != nil {
		return logclient.AlertCreate{}, diag.Diagnostics{diag.NewErrorDiagnostic("Invalid duration", fmt.Sprintf("frequency: %v", err))}
	}
	var ch []string
	if !m.ChannelIDs.IsNull() && !m.ChannelIDs.IsUnknown() {
		if diags := m.ChannelIDs.ElementsAs(ctx, &ch, false); diags.HasError() {
			return logclient.AlertCreate{}, diags
		}
	}
	var envs []string
	if !m.Environments.IsNull() && !m.Environments.IsUnknown() {
		if diags := m.Environments.ElementsAs(ctx, &envs, false); diags.HasError() {
			return logclient.AlertCreate{}, diags
		}
	}
	desc := ""
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		desc = m.Description.ValueString()
	}
	var active *bool
	if !m.Active.IsNull() && !m.Active.IsUnknown() {
		v := m.Active.ValueBool()
		active = &v
	}
	return logclient.AlertCreate{
		Name:         m.Name.ValueString(),
		Description:  &desc,
		Active:       active,
		Query:        m.Query.ValueString(),
		TimeWindow:   durToISO8601(tw),
		Frequency:    durToISO8601(fr),
		Watermark:    durToISO8601(defaultAlertWatermark),
		Environments: envs,
		ChannelIDs:   ch,
		NotifyWhen:   m.NotifyWhen.ValueString(),
	}, nil
}

func alertReadToModel(ctx context.Context, a *logclient.AlertRead, m *AlertModel) diag.Diagnostics {
	m.ID = types.StringValue(a.ID)
	if a.ProjectID != "" {
		m.ProjectID = types.StringValue(a.ProjectID)
	}
	m.Name = types.StringValue(a.Name)
	if a.Description == nil || (*a.Description == "" && (m.Description.IsNull() || m.Description.IsUnknown())) {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*a.Description)
	}
	m.Query = types.StringValue(a.Query)

	if d, err := iso8601ToDuration(a.TimeWindow); err == nil {
		m.TimeWindow = types.StringValue(durationCompact(d))
	} else {
		m.TimeWindow = types.StringValue("")
	}
	if d, err := iso8601ToDuration(a.Frequency); err == nil {
		m.Frequency = types.StringValue(durationCompact(d))
	} else {
		m.Frequency = types.StringValue("")
	}
	if d, err := iso8601ToDuration(a.Watermark); err == nil {
		m.Watermark = types.StringValue(durationCompact(d))
	} else {
		m.Watermark = types.StringValue("")
	}

	// The API returns an empty list when the alert is not scoped to any
	// environment; keep the attribute null when the config omitted it so an
	// omitted attribute and an empty set round-trip without spurious diffs.
	if len(a.Environments) == 0 && (m.Environments.IsNull() || m.Environments.IsUnknown()) {
		m.Environments = types.SetNull(types.StringType)
	} else {
		envSet, diags := types.SetValueFrom(ctx, types.StringType, a.Environments)
		if diags.HasError() {
			return diags
		}
		m.Environments = envSet
	}

	ch := make([]string, 0, len(a.Channels))
	for _, channel := range a.Channels {
		ch = append(ch, channel.ID)
	}
	set, diags := types.SetValueFrom(ctx, types.StringType, ch)
	if diags.HasError() {
		return diags
	}
	m.ChannelIDs = set
	m.NotifyWhen = types.StringValue(a.NotifyWhen)
	m.Active = types.BoolValue(a.Active)
	return nil
}

// --- CRUD ---

func (r *AlertResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan AlertModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ProjectID.IsNull() || plan.ProjectID.IsUnknown() || plan.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "The alert requires a project_id to construct API paths.")
		return
	}

	projectID := plan.ProjectID.ValueString()
	in, diags := alertModelToCreate(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateAlert(ctx, projectID, in)
	if err != nil {
		resp.Diagnostics.AddError("Create alert failed", err.Error())
		return
	}

	// Refetch alert to populate channel IDs.
	fresh, _, gerr := r.client.GetAlert(ctx, projectID, out.ID)
	if gerr != nil {
		fresh = out
	}

	state := plan
	state.ProjectID = plan.ProjectID
	if diags := alertReadToModel(ctx, fresh, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = plan.ProjectID
	}

	tflog.Trace(ctx, "created alert", map[string]any{"id": state.ID.ValueString(), "project_id": projectID})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state AlertModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot read alert because the state is missing a project_id.")
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	projectID := state.ProjectID.ValueString()
	id := state.ID.ValueString()

	out, status, err := r.client.GetAlert(ctx, projectID, id)
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		if projectMissing(ctx, r.client, projectID) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read alert failed", err.Error())
		return
	}

	if diags := alertReadToModel(ctx, out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = types.StringValue(projectID)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan AlertModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state AlertModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot update alert because the current state has no ID.")
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot update alert because the current state has no project_id.")
		return
	}

	projectID := state.ProjectID.ValueString()
	id := state.ID.ValueString()

	var payload logclient.AlertUpdate
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		v := plan.Name.ValueString()
		payload.Name = &v
	}
	if !plan.Description.IsUnknown() {
		switch {
		case plan.Description.IsNull():
			if !state.Description.IsNull() && !state.Description.IsUnknown() {
				empty := ""
				payload.Description = &empty
			}
		default:
			v := plan.Description.ValueString()
			if state.Description.IsNull() || state.Description.IsUnknown() || state.Description.ValueString() != v {
				payload.Description = &v
			}
		}
	} else if !state.Description.IsNull() && !state.Description.IsUnknown() {
		v := state.Description.ValueString()
		payload.Description = &v
	}
	if !plan.Active.IsNull() && !plan.Active.IsUnknown() {
		v := plan.Active.ValueBool()
		payload.Active = &v
	}
	if !plan.Query.IsNull() && !plan.Query.IsUnknown() {
		v := plan.Query.ValueString()
		payload.Query = &v
	}

	if !plan.TimeWindow.IsNull() && !plan.TimeWindow.IsUnknown() {
		d, err := parseDurationStr(plan.TimeWindow)
		if err != nil {
			resp.Diagnostics.AddError("Invalid time_window", err.Error())
			return
		}
		v := durToISO8601(d)
		payload.TimeWindow = &v
	}
	if !plan.Frequency.IsNull() && !plan.Frequency.IsUnknown() {
		d, err := parseDurationStr(plan.Frequency)
		if err != nil {
			resp.Diagnostics.AddError("Invalid frequency", err.Error())
			return
		}
		v := durToISO8601(d)
		payload.Frequency = &v
	}

	if !plan.Environments.IsUnknown() {
		switch {
		case plan.Environments.IsNull():
			if !state.Environments.IsNull() && !state.Environments.IsUnknown() {
				// Attribute removed from config: clear the environment filter.
				empty := []string{}
				payload.Environments = &empty
			}
		default:
			var envs []string
			if diags := plan.Environments.ElementsAs(ctx, &envs, false); diags.HasError() {
				resp.Diagnostics.Append(diags...)
				return
			}
			if envs == nil {
				envs = []string{}
			}
			payload.Environments = &envs
		}
	}

	// For sets, send only when we actually have values in the plan.
	if !plan.ChannelIDs.IsNull() && !plan.ChannelIDs.IsUnknown() {
		var ids []string
		if diags := plan.ChannelIDs.ElementsAs(ctx, &ids, false); diags.HasError() {
			resp.Diagnostics.Append(diags...)
			return
		}
		payload.ChannelIDs = &ids
	}

	if !plan.NotifyWhen.IsNull() && !plan.NotifyWhen.IsUnknown() {
		v := plan.NotifyWhen.ValueString()
		payload.NotifyWhen = &v
	}

	out, err := r.client.UpdateAlert(ctx, projectID, id, payload)
	if err != nil {
		resp.Diagnostics.AddError("Update alert failed", err.Error())
		return
	}

	newState := plan
	newState.ProjectID = state.ProjectID
	if diags := alertReadToModel(ctx, out, &newState); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if newState.ProjectID.IsNull() || newState.ProjectID.IsUnknown() || newState.ProjectID.ValueString() == "" {
		newState.ProjectID = state.ProjectID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *AlertResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state AlertModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot delete alert because the state is missing a project_id.")
		return
	}

	projectID := state.ProjectID.ValueString()
	id := state.ID.ValueString()

	if err := r.client.DeleteAlert(ctx, projectID, id); err != nil {
		if logclient.IsNotFoundError(err) {
			// Already gone, treat as successful delete
			return
		}
		if projectMissing(ctx, r.client, projectID) {
			return
		}
		resp.Diagnostics.AddError("Delete alert failed", err.Error())
	}
}

func (r *AlertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	rawID := strings.TrimSpace(req.ID)
	if rawID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use either "project_id/alert_id" or "project_name/alert_name" (also accepts "," or "|").`,
		)
		return
	}

	parts, err := splitImportParts(rawID, 2)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "project_id/alert_id" or "project_name/alert_name" (also accepts "project,alert" or "project|alert").`,
		)
		return
	}

	projectKey := parts[0]
	alertKey := parts[1]

	projectID, projectName, err := findProjectByNameOrID(ctx, r.client, projectKey)
	if err != nil {
		resp.Diagnostics.AddError("Import alert failed", err.Error())
		return
	}

	alerts, err := r.client.ListAlerts(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Import alert failed", fmt.Sprintf("listing alerts for project %q: %v", projectID, err))
		return
	}

	var (
		match       *logclient.AlertRead
		nameMatches []*logclient.AlertRead
	)
	for i := range alerts {
		a := &alerts[i]
		if a.ID == alertKey {
			match = a
			break
		}
		if a.Name == alertKey {
			nameMatches = append(nameMatches, a)
		}
	}
	if match == nil {
		if len(nameMatches) == 0 {
			resp.Diagnostics.AddError("Import alert failed", fmt.Sprintf("alert %q not found in project %q", alertKey, projectName))
			return
		}
		match = nameMatches[0]
		if len(nameMatches) > 1 {
			resp.Diagnostics.AddWarning("Import alert", fmt.Sprintf("multiple alerts named %q in project %q; imported the first match", alertKey, projectName))
		}
	}

	alert, _, err := r.client.GetAlert(ctx, projectID, match.ID)
	if err != nil {
		resp.Diagnostics.AddError("Import alert failed", fmt.Sprintf("fetching alert %q: %v", match.ID, err))
		return
	}

	var state AlertModel
	state.ProjectID = types.StringValue(projectID)
	if diags := alertReadToModel(ctx, alert, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		state.ProjectID = types.StringValue(projectID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
