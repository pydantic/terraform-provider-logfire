package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
)

var _ resource.Resource = &AlertResource{}
var _ resource.ResourceWithImportState = &AlertResource{}

func NewAlertResource() resource.Resource { return &AlertResource{} }

type AlertResource struct {
	client *APIClient
}

type AlertModel struct {
	ID           types.String   `tfsdk:"id"`
	Organization types.String   `tfsdk:"organization"`
	Project      types.String   `tfsdk:"project"`
	Name         types.String   `tfsdk:"name"`
	Description  types.String   `tfsdk:"description"`
	Query        types.String   `tfsdk:"query"`
	TimeWindow   types.String   `tfsdk:"time_window"`
	Frequency    types.String   `tfsdk:"frequency"`
	Watermark    types.String   `tfsdk:"watermark"`
	ChannelIDs   []types.String `tfsdk:"channel_ids"`
	NotifyWhen   types.String   `tfsdk:"notify_when"`
	Active       types.Bool     `tfsdk:"active"`
}

func (r *AlertResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert"
}

func (r *AlertResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	time_constraint := []string{"1m", "2m", "5m", "10m", "15m", "30m", "1h", "6h", "12h", "24h"}
	notification_constraint := []string{"has_matches", "has_matches_changed", "matches_changed"}

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
			"organization": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Organization name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alert name (unique per project).",
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
				MarkdownDescription: "Lookback window as Go duration ",
				Validators: []validator.String{
					stringvalidator.OneOf(time_constraint...),
				},
			},
			"frequency": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Evaluation frequency as Go duration",
				Validators: []validator.String{
					stringvalidator.OneOf(time_constraint...),
				},
			},
			"watermark": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Watermark (lateness tolerance) as Go duration.",
			},
			"channel_ids": rschema.ListAttribute{
				ElementType:         types.StringType,
				Required:            true,
				MarkdownDescription: "List of channel IDs to notify.",
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
	c, ok := req.ProviderData.(*APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *APIClient, got %T", req.ProviderData))
		return
	}
	r.client = c
}

// --- helpers ---

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
		b.WriteString(fmt.Sprintf("%dH", h))
	}
	if m > 0 {
		b.WriteString(fmt.Sprintf("%dM", m))
	}
	if s > 0 || (h == 0 && m == 0) {
		b.WriteString(fmt.Sprintf("%dS", s))
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
	min, err := parse(m[3])
	if err != nil {
		return 0, err
	}
	sec, err := parse(m[4])
	if err != nil {
		return 0, err
	}

	return time.Duration(days)*24*time.Hour +
		time.Duration(h)*time.Hour +
		time.Duration(min)*time.Minute +
		time.Duration(sec)*time.Second, nil
}

func durationCompact(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = -d
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

func parseDurationStr(s types.String) (time.Duration, error) {
	// Interpret as Go duration string (e.g. "5m30s")
	return time.ParseDuration(s.ValueString())
}

func modelToCreate(m *AlertModel) (AlertCreate, error) {
	tw, err := parseDurationStr(m.TimeWindow)
	if err != nil {
		return AlertCreate{}, fmt.Errorf("time_window: %w", err)
	}
	fr, err := parseDurationStr(m.Frequency)
	if err != nil {
		return AlertCreate{}, fmt.Errorf("frequency: %w", err)
	}
	wm, err := parseDurationStr(m.Watermark)
	if err != nil {
		return AlertCreate{}, fmt.Errorf("watermark: %w", err)
	}

	ch := make([]string, 0, len(m.ChannelIDs))
	for _, v := range m.ChannelIDs {
		ch = append(ch, v.ValueString())
	}
	return AlertCreate{
		Name:        m.Name.ValueString(),
		Description: m.Description.ValueString(),
		Query:       m.Query.ValueString(),
		TimeWindow:  durToISO8601(tw),
		Frequency:   durToISO8601(fr),
		Watermark:   durToISO8601(wm),
		ChannelIDs:  ch,
		NotifyWhen:  m.NotifyWhen.ValueString(),
	}, nil
}

func readToModel(a *AlertRead, m *AlertModel) {
	m.ID = types.StringValue(a.ID)
	m.Name = types.StringValue(a.Name)
	m.Description = types.StringValue(a.Description)
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

	m.ChannelIDs = make([]types.String, 0, len(a.ChannelIDs))
	for _, cid := range a.ChannelIDs {
		m.ChannelIDs = append(m.ChannelIDs, types.StringValue(cid))
	}
	m.NotifyWhen = types.StringValue(a.NotifyWhen)
	m.Active = types.BoolValue(a.Active)
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

	org := plan.Organization.ValueString()
	prj := plan.Project.ValueString()

	in, err := modelToCreate(&plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid duration", err.Error())
		return
	}

	out, err := r.client.CreateAlert(ctx, org, prj, in)
	if err != nil {
		resp.Diagnostics.AddError("Create alert failed", err.Error())
		return
	}

	var state AlertModel
	// Preserve org/project from plan into state
	state.Organization = plan.Organization
	state.Project = plan.Project
	readToModel(out, &state)

	tflog.Trace(ctx, "created alert", map[string]any{"id": state.ID.ValueString(), "org": org, "project": prj})
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

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()

	out, status, err := r.client.GetAlert(ctx, org, prj, state.ID.ValueString())
	if err != nil {
		if status == 404 {
			// Removed out-of-band
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read alert failed", err.Error())
		return
	}

	// keep org/project
	orgVal := state.Organization
	prjVal := state.Project

	readToModel(out, &state)
	state.Organization = orgVal
	state.Project = prjVal

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

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()

	// Build partial update payload
	var payload AlertUpdate

	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		v := plan.Name.ValueString()
		payload.Name = &v
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		v := plan.Description.ValueString()
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

	if !plan.Watermark.IsNull() && !plan.Watermark.IsUnknown() {
		d, err := parseDurationStr(plan.Watermark)
		if err != nil {
			resp.Diagnostics.AddError("Invalid watermark", err.Error())
			return
		}
		v := durToISO8601(d)
		payload.Watermark = &v
	}

	// For lists, send only when we actually have values in the plan.
	if plan.ChannelIDs != nil {
		ids := make([]string, 0, len(plan.ChannelIDs))
	 for _, cid := range plan.ChannelIDs {
			if !cid.IsNull() && !cid.IsUnknown() {
				ids = append(ids, cid.ValueString())
			}
		}
		payload.ChannelIDs = &ids
	}

	if !plan.NotifyWhen.IsNull() && !plan.NotifyWhen.IsUnknown() {
		v := plan.NotifyWhen.ValueString()
		payload.NotifyWhen = &v
	}

	out, err := r.client.UpdateAlert(ctx, org, prj, state.ID.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update alert failed", err.Error())
		return
	}

	var newState AlertModel
	// keep org/project
	newState.Organization = state.Organization
	newState.Project = state.Project
	readToModel(out, &newState)

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
	org := state.Organization.ValueString()
	prj := state.Project.ValueString()

	if err := r.client.DeleteAlert(ctx, org, prj, state.ID.ValueString()); err != nil {
		// If already gone, treat as successful delete but surface info.
		resp.Diagnostics.AddWarning("Delete alert", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *AlertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Support "organization/project/id" (preferred) or just "id" (legacy).
	parts := strings.Split(req.ID, "/")
	switch len(parts) {
	case 3:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), parts[1])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
	case 1:
		// Only ID provided – user must already have org/project in config/state.
		resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	default:
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "organization/project/id" or "id".`,
		)
	}
}
