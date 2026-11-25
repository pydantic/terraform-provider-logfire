// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

type AlertResource struct{ configured }

// Absolute bounds accepted by the API for alert durations. These are the only
// limits that hold regardless of the other field; the API additionally caps
// time_window based on frequency (for example a 30d window needs a daily
// evaluation) and reports that itself.
const (
	alertTimeWindowMin = time.Minute
	alertTimeWindowMax = 30 * 24 * time.Hour
	alertFrequencyMin  = time.Minute
	alertFrequencyMax  = 24 * time.Hour
)

type AlertModel struct {
	ID           types.String       `tfsdk:"id"`
	ProjectID    types.String       `tfsdk:"project_id"`
	Name         types.String       `tfsdk:"name"`
	Description  types.String       `tfsdk:"description"`
	Query        types.String       `tfsdk:"query"`
	TimeWindow   alertDurationValue `tfsdk:"time_window"`
	Frequency    alertDurationValue `tfsdk:"frequency"`
	Watermark    types.String       `tfsdk:"watermark"`
	Environments types.Set          `tfsdk:"environments"`
	NotifyWhen   types.String       `tfsdk:"notify_when"`
	Active       types.Bool         `tfsdk:"active"`
	// ChannelAssignments uses the same schema, model and conversion as
	// `logfire_slo.alerts.<tier>.channel_assignments`.
	ChannelAssignments types.Set                 `tfsdk:"channel_assignments"`
	InstancePolicy     *AlertInstancePolicyModel `tfsdk:"instance_policy"`
}

type AlertInstancePolicyModel struct {
	IdentityColumns []types.String `tfsdk:"identity_columns"`
	PendingRuns     types.Int64    `tfsdk:"pending_runs"`
	MissingRuns     types.Int64    `tfsdk:"missing_runs"`
	MissingBehavior types.String   `tfsdk:"missing_behavior"`
}

func (r *AlertResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert"
}

func (r *AlertResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire alert.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Alert ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project ID (UUID) used for alert API paths.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alert name (unique per project).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
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
				CustomType:          alertDurationType{},
				Required:            true,
				MarkdownDescription: "Lookback window, as a duration from `1m` to `30d` (for example `20m`, `1h30m`, `7d`). The API caps this relative to `frequency` - a longer window requires a less frequent evaluation - and reports that itself. Equivalent spellings are accepted and kept as written.",
				Validators: []validator.String{
					alertDurationValidator{min: alertTimeWindowMin, max: alertTimeWindowMax},
				},
			},
			"frequency": rschema.StringAttribute{
				CustomType:          alertDurationType{},
				Required:            true,
				MarkdownDescription: "Evaluation frequency, as a duration from `1m` to `24h` (for example `5m`, `20m`, `1h`). Equivalent spellings are accepted and kept as written.",
				Validators: []validator.String{
					alertDurationValidator{min: alertFrequencyMin, max: alertFrequencyMax},
				},
			},
			"watermark": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Provider-managed watermark (lateness tolerance) sent to the API.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"environments": rschema.SetAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Deployment environments to scope the query to. Empty = all environments (no filter).",
			},
			"channel_assignments": channelAssignmentsAttribute(
				"Channels to notify, each with an optional delivery schedule. Set it to `[]` to notify no channel. This is the same type as `alerts.<tier>.channel_assignments` on `logfire_slo`, so one value (for example a `locals` entry) can configure both.",
				channelAssignmentsRequired,
			),
			"notify_when": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Notification rule. Must match API enum. `instances_changed` needs `instance_policy`. It sends one notification for each run that opens or closes occurrences, listing them, from Logfire v2026-11-13.01.",
				Validators: []validator.String{
					stringvalidator.OneOf(
						"has_matches", "has_matches_changed", "matches_changed",
						"starts_having_matches", "instances_changed",
					),
				},
			},
			"active": rschema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the alert is active (defaults to true on creation).",
			},
			"instance_policy": rschema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Track each result row as its own occurrence, keyed by the identity columns, instead of evaluating the whole result as one. Needs an organization with alert instance policies enabled, otherwise the API answers 403 `alert_instances_not_enabled`. Remove the attribute to evaluate the whole result as one again. The API's deprecated `continuity_runs` is not exposed. A new policy gets the value of `missing_runs`, and a change keeps the stored value unless it is lower than `missing_runs`.",
				Attributes: map[string]rschema.Attribute{
					"identity_columns": rschema.ListAttribute{
						ElementType:         types.StringType,
						Required:            true,
						MarkdownDescription: "Result columns whose values identify an occurrence. One to 8 unique names of 1 to 128 characters.",
						Validators: []validator.List{
							listvalidator.SizeBetween(1, 8),
							listvalidator.UniqueValues(),
							listvalidator.ValueStringsAre(
								stringvalidator.UTF8LengthBetween(1, 128),
							),
						},
					},
					"pending_runs": rschema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Consecutive matching runs, 1 to 100, before a new occurrence fires.",
						Validators:          []validator.Int64{int64validator.Between(1, 100)},
					},
					"missing_runs": rschema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Consecutive runs without the occurrence, 1 to 100, before it closes.",
						Validators:          []validator.Int64{int64validator.Between(1, 100)},
					},
					"missing_behavior": rschema.StringAttribute{
						Required:            true,
						MarkdownDescription: "The close reason of an occurrence that is missing for `missing_runs` runs. One of `no_longer_matching` or `expire`.",
						Validators:          []validator.String{stringvalidator.OneOf("no_longer_matching", "expire")},
					},
				},
			},
		},
	}
}

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
	var n [4]int
	for i, x := range m[1:] {
		if x == "" {
			continue
		}
		var err error
		if n[i], err = strconv.Atoi(x); err != nil {
			return 0, err
		}
	}
	return time.Duration(n[0])*24*time.Hour +
		time.Duration(n[1])*time.Hour +
		time.Duration(n[2])*time.Minute +
		time.Duration(n[3])*time.Second, nil
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

func parseDurationText(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	// Also accept the day shorthand of the provider schema, such as "7d".
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf("invalid duration: %q", raw)
}

func alertInstancePolicyToAPI(m *AlertInstancePolicyModel) *logclient.AlertInstancePolicy {
	if m == nil {
		return nil
	}
	columns := make([]string, len(m.IdentityColumns))
	for i, column := range m.IdentityColumns {
		columns[i] = column.ValueString()
	}
	return &logclient.AlertInstancePolicy{
		IdentityColumns: columns,
		PendingRuns:     m.PendingRuns.ValueInt64(),
		MissingRuns:     m.MissingRuns.ValueInt64(),
		MissingBehavior: m.MissingBehavior.ValueString(),
		ContinuityRuns:  m.MissingRuns.ValueInt64(),
	}
}

// addAlertSaveError names the missing opt-in when the API refuses an instance
// policy, instead of showing its raw problem body.
func addAlertSaveError(diags *diag.Diagnostics, summary string, err error) {
	var apiErr *logclient.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden &&
		strings.Contains(apiErr.Message, "alert_instances_not_enabled") {
		diags.AddAttributeError(
			path.Root("instance_policy"),
			"Alert instance policies are not enabled",
			"The API refused instance_policy (403 alert_instances_not_enabled): this organization has not enabled instance policies. Ask Logfire support to enable them, or remove instance_policy and use another notify_when value.",
		)
		return
	}
	diags.AddError(summary, err.Error())
}

// addIgnoredPolicyError reports a Logfire version without instance policies,
// which saves the alert and drops instance_policy. The caller still saves the
// alert Logfire returned.
func addIgnoredPolicyError(diags *diag.Diagnostics, planned *AlertInstancePolicyModel, saved *logclient.AlertRead) {
	if planned != nil && saved.InstancePolicy == nil {
		diags.AddAttributeError(
			path.Root("instance_policy"),
			"Logfire ignored instance_policy",
			"This Logfire version does not support alert instance policies, so it saved the alert without one. Upgrade Logfire, or remove instance_policy.",
		)
	}
}

// alertDurations parses time_window and frequency. Upjet can apply without
// running the config validators, so a bad value fails here instead of being
// sent as zero.
func alertDurations(m *AlertModel) (time.Duration, time.Duration, diag.Diagnostics) {
	var diags diag.Diagnostics
	tw, err := parseDurationText(m.TimeWindow.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("time_window"), "Invalid time_window", err.Error())
	}
	fr, err := parseDurationText(m.Frequency.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("frequency"), "Invalid frequency", err.Error())
	}
	return tw, fr, diags
}

func alertModelToCreate(ctx context.Context, m *AlertModel) (logclient.AlertCreate, diag.Diagnostics) {
	tw, fr, diags := alertDurations(m)
	if diags.HasError() {
		return logclient.AlertCreate{}, diags
	}
	ch, diags := channelAssignmentsToAPI(ctx, m.ChannelAssignments)
	if diags.HasError() {
		return logclient.AlertCreate{}, diags
	}
	var envs []string
	if !m.Environments.IsNull() {
		if diags := m.Environments.ElementsAs(ctx, &envs, false); diags.HasError() {
			return logclient.AlertCreate{}, diags
		}
	}
	var active *bool
	if !m.Active.IsUnknown() {
		active = m.Active.ValueBoolPointer()
	}
	return logclient.AlertCreate{
		Name:               m.Name.ValueString(),
		Description:        new(m.Description.ValueString()),
		Active:             active,
		Query:              m.Query.ValueString(),
		TimeWindow:         durToISO8601(tw),
		Frequency:          durToISO8601(fr),
		Watermark:          durToISO8601(defaultAlertWatermark),
		Environments:       envs,
		ChannelAssignments: ch,
		NotifyWhen:         m.NotifyWhen.ValueString(),
		InstancePolicy:     alertInstancePolicyToAPI(m.InstancePolicy),
	}, nil
}

func alertReadToModel(ctx context.Context, a *logclient.AlertRead, m *AlertModel) diag.Diagnostics {
	m.ID = types.StringValue(a.ID)
	if a.ProjectID != "" {
		m.ProjectID = types.StringValue(a.ProjectID)
	}
	m.Name = types.StringValue(a.Name)
	if a.Description == nil || (*a.Description == "" && m.Description.IsNull()) {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*a.Description)
	}
	m.Query = types.StringValue(a.Query)

	compact := func(iso string) string {
		d, err := iso8601ToDuration(iso)
		if err != nil {
			return ""
		}
		return durationCompact(d)
	}
	m.TimeWindow = newAlertDurationValue(compact(a.TimeWindow))
	m.Frequency = newAlertDurationValue(compact(a.Frequency))
	m.Watermark = types.StringValue(compact(a.Watermark))

	// The API returns an empty list when the alert is not scoped to any
	// environment; keep the attribute null when the config omitted it so an
	// omitted attribute and an empty set round-trip without spurious diffs.
	if len(a.Environments) == 0 && m.Environments.IsNull() {
		m.Environments = types.SetNull(types.StringType)
	} else {
		envSet, diags := types.SetValueFrom(ctx, types.StringType, a.Environments)
		if diags.HasError() {
			return diags
		}
		m.Environments = envSet
	}

	// Logfire releases before v2026-09-23.01 return an alert's assignments
	// only inside `channels`, so rebuild them from there when
	// `channel_assignments` is absent.
	assignments := a.ChannelAssignments
	if assignments == nil {
		assignments = make([]logclient.ChannelAssignment, 0, len(a.Channels))
		for _, channel := range a.Channels {
			assignments = append(assignments, logclient.ChannelAssignment{
				ChannelID: channel.ID, ScheduleID: channel.ScheduleID,
			})
		}
	}
	set, diags := channelAssignmentsFromAPI(ctx, assignments)
	if diags.HasError() {
		return diags
	}
	m.ChannelAssignments = set
	m.NotifyWhen = types.StringValue(a.NotifyWhen)
	m.Active = types.BoolValue(a.Active)
	m.InstancePolicy = nil
	if p := a.InstancePolicy; p != nil {
		columns := make([]types.String, len(p.IdentityColumns))
		for i, column := range p.IdentityColumns {
			columns[i] = types.StringValue(column)
		}
		m.InstancePolicy = &AlertInstancePolicyModel{
			IdentityColumns: columns,
			PendingRuns:     types.Int64Value(p.PendingRuns),
			MissingRuns:     types.Int64Value(p.MissingRuns),
			MissingBehavior: types.StringValue(p.MissingBehavior),
		}
	}
	return nil
}

func (r *AlertResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AlertModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
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
		addAlertSaveError(&resp.Diagnostics, "Create alert failed", err)
		return
	}

	// Refetch alert to populate its channels.
	if fresh, _, err := r.client.GetAlert(ctx, projectID, out.ID); err == nil {
		out = fresh
	}

	state := plan
	if diags := alertReadToModel(ctx, out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	addIgnoredPolicyError(&resp.Diagnostics, plan.InstancePolicy, out)

	tflog.Trace(ctx, "created alert", map[string]any{
		"id": state.ID.ValueString(), "project_id": projectID,
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AlertModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	projectID := state.ProjectID.ValueString()
	out, status, err := r.client.GetAlert(ctx, projectID, state.ID.ValueString())
	if err != nil {
		if status == 404 || projectMissing(ctx, r.client, projectID, err) {
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AlertModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if missingID(state.ID, &resp.Diagnostics) {
		return
	}

	projectID := state.ProjectID.ValueString()
	id := state.ID.ValueString()

	tw, fr, diags := alertDurations(&plan)
	resp.Diagnostics.Append(diags...)
	if diags.HasError() {
		return
	}
	payload := logclient.AlertUpdate{
		Name:       plan.Name.ValueStringPointer(),
		Query:      plan.Query.ValueStringPointer(),
		TimeWindow: new(durToISO8601(tw)),
		Frequency:  new(durToISO8601(fr)),
		NotifyWhen: plan.NotifyWhen.ValueStringPointer(),
	}
	if !plan.Description.Equal(state.Description) {
		payload.Description = new(plan.Description.ValueString())
	}
	if !plan.Active.IsUnknown() {
		payload.Active = plan.Active.ValueBoolPointer()
	}

	// Removing the attribute from the config clears the environment filter.
	if !plan.Environments.IsNull() || !state.Environments.IsNull() {
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

	assignments, diags := channelAssignmentsToAPI(ctx, plan.ChannelAssignments)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	payload.ChannelAssignments = &assignments

	// An unchanged policy is left out so the API keeps it. A removed one is
	// sent as null.
	want := alertInstancePolicyToAPI(plan.InstancePolicy)
	if !reflect.DeepEqual(want, alertInstancePolicyToAPI(state.InstancePolicy)) {
		// Logfire releases through v2026-11-05.01 use continuity_runs, so a change
		// keeps the stored value. The API refuses one below missing_runs.
		if want != nil && state.InstancePolicy != nil {
			current, _, err := r.client.GetAlert(ctx, projectID, id)
			if err != nil {
				resp.Diagnostics.AddError("Read alert failed", err.Error())
				return
			}
			if p := current.InstancePolicy; p != nil && p.ContinuityRuns >= want.MissingRuns {
				want.ContinuityRuns = p.ContinuityRuns
			}
		}
		payload.InstancePolicy = &want
	}

	out, err := r.client.UpdateAlert(ctx, projectID, id, payload)
	if err != nil {
		addAlertSaveError(&resp.Diagnostics, "Update alert failed", err)
		return
	}

	newState := plan
	if diags := alertReadToModel(ctx, out, &newState); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	addIgnoredPolicyError(&resp.Diagnostics, plan.InstancePolicy, out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *AlertResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AlertModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if missingID(state.ID, &resp.Diagnostics) {
		return
	}

	projectID := state.ProjectID.ValueString()
	err := r.client.DeleteAlert(ctx, projectID, state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) && !projectMissing(ctx, r.client, projectID, err) {
		resp.Diagnostics.AddError("Delete alert failed", err.Error())
	}
}

func (r *AlertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	projectID, match, ok := importProjectItem(ctx, r.client, req, resp, projectImport[logclient.AlertRead]{
		noun:      "alert",
		counts:    []int{2},
		missing:   `Expected a non-empty ID. Use either "project_id/alert_id" or "project_name/alert_name" (also accepts "," or "|").`,
		invalid:   `Expected "project_id/alert_id" or "project_name/alert_name" (also accepts "project,alert" or "project|alert").`,
		list:      r.client.ListAlerts,
		keys:      func(a *logclient.AlertRead) (string, string) { return a.ID, a.Name },
		duplicate: "multiple alerts named %q in project %q; imported the first match",
	})
	if !ok {
		return
	}

	alert, _, err := r.client.GetAlert(ctx, projectID, match.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Import alert failed", fmt.Sprintf("fetching alert %q: %v", match.ID, err),
		)
		return
	}

	state := AlertModel{ProjectID: types.StringValue(projectID)}
	if diags := alertReadToModel(ctx, alert, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
