// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	int64validator "github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	listvalidator "github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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

var _ resource.Resource = &ScheduleResource{}
var _ resource.ResourceWithConfigure = &ScheduleResource{}
var _ resource.ResourceWithImportState = &ScheduleResource{}

func NewScheduleResource() resource.Resource { return &ScheduleResource{} }

type ScheduleResource struct {
	client *logclient.APIClient
}

type ScheduleModel struct {
	ID       types.String `tfsdk:"id"`
	Label    types.String `tfsdk:"label"`
	Timezone types.String `tfsdk:"timezone"`
	Windows  types.List   `tfsdk:"windows"`
}

type scheduleWindowModel struct {
	Days      types.List   `tfsdk:"days"`
	StartTime types.String `tfsdk:"start_time"`
	EndTime   types.String `tfsdk:"end_time"`
}

var scheduleWindowObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"days":       types.ListType{ElemType: types.Int64Type},
	"start_time": types.StringType,
	"end_time":   types.StringType,
}}

var scheduleTimeRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func (r *ScheduleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_schedule"
}

func (r *ScheduleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	timeValidators := []validator.String{
		stringvalidator.RegexMatches(scheduleTimeRe, "must be a 24-hour time in HH:MM format"),
	}
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages an organization-level Logfire delivery schedule. " +
			"A channel assignment with a `schedule_id` notifies its channel only inside the schedule's windows. " +
			"Use it in `channel_assignments` on `logfire_alert` and in `alerts.<tier>.channel_assignments` on `logfire_slo`. " +
			"Deleting a schedule makes the channel assignments that use it notify at all times. " +
			"The provider credential needs `organization:read_channel` and `organization:write_channel`. " +
			"The schedules API needs " + sloDeliveryRelease + ".",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Schedule ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"label": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Schedule name.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"timezone": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "IANA time zone of the windows, for example `Europe/London`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"windows": rschema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "Time windows in which the schedule delivers. At least one.",
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: rschema.NestedAttributeObject{
					Attributes: map[string]rschema.Attribute{
						"days": rschema.ListAttribute{
							ElementType:         types.Int64Type,
							Required:            true,
							MarkdownDescription: "ISO weekday numbers: `1` is Monday and `7` is Sunday.",
							Validators: []validator.List{
								listvalidator.SizeAtLeast(1),
								listvalidator.UniqueValues(),
								listvalidator.ValueInt64sAre(int64validator.Between(1, 7)),
							},
						},
						"start_time": rschema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Start of the window, as 24-hour `HH:MM` in `timezone`.",
							Validators:          timeValidators,
						},
						"end_time": rschema.StringAttribute{
							Required:            true,
							MarkdownDescription: "End of the window, as 24-hour `HH:MM` in `timezone`.",
							Validators:          timeValidators,
						},
					},
				},
			},
		},
	}
}

func (r *ScheduleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func scheduleWindowsToAPI(ctx context.Context, windows types.List) ([]logclient.ScheduleWindow, diag.Diagnostics) {
	var models []scheduleWindowModel
	if diags := windows.ElementsAs(ctx, &models, false); diags.HasError() {
		return nil, diags
	}
	out := make([]logclient.ScheduleWindow, 0, len(models))
	for _, w := range models {
		var days []int64
		if diags := w.Days.ElementsAs(ctx, &days, false); diags.HasError() {
			return nil, diags
		}
		out = append(out, logclient.ScheduleWindow{
			Days:      days,
			StartTime: w.StartTime.ValueString(),
			EndTime:   w.EndTime.ValueString(),
		})
	}
	return out, nil
}

// parseScheduleTime accepts the "HH:MM" form the schema takes and the
// "HH:MM:SS" form the API returns.
func parseScheduleTime(s string) (time.Time, bool) {
	for _, layout := range []string{"15:04", "15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// scheduleTimeValue returns the configured spelling when it denotes the same
// time as the API value, and otherwise the API value as "HH:MM" (or
// "HH:MM:SS" when it has seconds).
func scheduleTimeValue(api string, prior types.String) types.String {
	t, ok := parseScheduleTime(api)
	if !ok {
		return types.StringValue(api)
	}
	if !prior.IsNull() && !prior.IsUnknown() {
		if p, ok := parseScheduleTime(prior.ValueString()); ok && p.Equal(t) {
			return prior
		}
	}
	if t.Second() != 0 {
		return types.StringValue(t.Format("15:04:05"))
	}
	return types.StringValue(t.Format("15:04"))
}

func scheduleReadToModel(ctx context.Context, s *logclient.ScheduleRead, m *ScheduleModel) diag.Diagnostics {
	var prior []scheduleWindowModel
	if !m.Windows.IsNull() && !m.Windows.IsUnknown() && m.Windows.ElementType(ctx) != nil {
		if diags := m.Windows.ElementsAs(ctx, &prior, false); diags.HasError() {
			return diags
		}
	}

	windows := make([]scheduleWindowModel, 0, len(s.Windows))
	for i, w := range s.Windows {
		priorStart, priorEnd := types.StringNull(), types.StringNull()
		if i < len(prior) {
			priorStart, priorEnd = prior[i].StartTime, prior[i].EndTime
		}
		days, diags := types.ListValueFrom(ctx, types.Int64Type, w.Days)
		if diags.HasError() {
			return diags
		}
		windows = append(windows, scheduleWindowModel{
			Days:      days,
			StartTime: scheduleTimeValue(w.StartTime, priorStart),
			EndTime:   scheduleTimeValue(w.EndTime, priorEnd),
		})
	}
	list, diags := types.ListValueFrom(ctx, scheduleWindowObjectType, windows)
	if diags.HasError() {
		return diags
	}

	m.ID = types.StringValue(s.ID)
	m.Label = types.StringValue(s.Label)
	m.Timezone = types.StringValue(s.Timezone)
	m.Windows = list
	return nil
}

// --- CRUD ---

func (r *ScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan ScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	windows, diags := scheduleWindowsToAPI(ctx, plan.Windows)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateSchedule(ctx, logclient.ScheduleCreate{
		Label:    plan.Label.ValueString(),
		Timezone: plan.Timezone.ValueString(),
		Windows:  windows,
	})
	if err != nil {
		resp.Diagnostics.AddError("Create schedule failed", err.Error())
		return
	}

	state := plan
	resp.Diagnostics.Append(scheduleReadToModel(ctx, out, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Trace(ctx, "created schedule", map[string]any{"id": state.ID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	out, status, err := r.client.GetSchedule(ctx, state.ID.ValueString())
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read schedule failed", err.Error())
		return
	}

	resp.Diagnostics.Append(scheduleReadToModel(ctx, out, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan, state ScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot update schedule because the current state has no ID.")
		return
	}

	windows, diags := scheduleWindowsToAPI(ctx, plan.Windows)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	label := plan.Label.ValueString()
	timezone := plan.Timezone.ValueString()

	out, err := r.client.UpdateSchedule(ctx, state.ID.ValueString(), logclient.ScheduleUpdate{
		Label:    &label,
		Timezone: &timezone,
		Windows:  &windows,
	})
	if err != nil {
		resp.Diagnostics.AddError("Update schedule failed", err.Error())
		return
	}

	newState := plan
	resp.Diagnostics.Append(scheduleReadToModel(ctx, out, &newState)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *ScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteSchedule(ctx, state.ID.ValueString()); err != nil {
		if logclient.IsNotFoundError(err) {
			// Already gone, treat as successful delete
			return
		}
		resp.Diagnostics.AddError("Delete schedule failed", err.Error())
	}
}

// ImportState imports a schedule by its UUID. The API has no list route, so
// there is no import by label. The schedule is fetched here so that an unknown
// ID fails the import instead of producing an empty state.
func (r *ScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected the schedule UUID. Example: terraform import logfire_schedule.office_hours "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"`,
		)
		return
	}

	out, status, err := r.client.GetSchedule(ctx, id)
	if err != nil {
		if status == 404 {
			resp.Diagnostics.AddError(
				"Import schedule failed",
				fmt.Sprintf("Schedule %q not found in the credential's organization. Import a schedule by its UUID.", id),
			)
			return
		}
		resp.Diagnostics.AddError("Import schedule failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), out.ID)...)
}
