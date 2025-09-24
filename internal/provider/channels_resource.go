package provider

import (
	"context"
	"fmt"
	"strings"
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

var _ resource.Resource = &ChannelResource{}
var _ resource.ResourceWithImportState = &ChannelResource{}

func NewChannelResource() resource.Resource { return &ChannelResource{} }

type ChannelResource struct {
	client *APIClient
}

type ChannelConfigModel struct {
	Type   types.String `tfsdk:"type"`   // must be "webhook"
	Format types.String `tfsdk:"format"` // "auto" | "slack-blockkit" | "slack-legacy" | "raw-data"
	URL    types.String `tfsdk:"url"`
}

type ChannelModel struct {
	ID           types.String        `tfsdk:"id"`
	Organization types.String        `tfsdk:"organization"`
	Project      types.String        `tfsdk:"project"`
	Label        types.String        `tfsdk:"label"`
	Config       *ChannelConfigModel `tfsdk:"config"`
}

func (r *ChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_channel"
}

func (r *ChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire channel.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Channel ID.",
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
			"label": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-friendly channel label.",
			},
		},
		Blocks: map[string]rschema.Block{
			"config": rschema.SingleNestedBlock{
				MarkdownDescription: "Channel configuration.",
				Attributes: map[string]rschema.Attribute{
					"type": rschema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Channel type. Only `webhook` is supported.",
						Validators: []validator.String{
							stringvalidator.OneOf("webhook"),
						},
					},
					"format": rschema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Payload format.",
						Validators: []validator.String{
							stringvalidator.OneOf("auto", "slack-blockkit", "slack-legacy", "raw-data"),
						},
					},
					"url": rschema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Webhook URL endpoint.",
					},
				},
			},
		},
	}
}

func (r *ChannelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ---- Channel Helpers ----

func channelModelToCreate(m *ChannelModel) ChannelCreate {
	cfg := ChannelConfig{
		Type:   m.Config.Type.ValueString(),
		Format: m.Config.Format.ValueString(),
		URL:    m.Config.URL.ValueString(),
	}
	return ChannelCreate{
		Label:  m.Label.ValueString(),
		Config: cfg,
	}
}

func channelReadToModel(in *ChannelRead, out *ChannelModel) {
	out.ID = types.StringValue(in.ID)
	out.Label = types.StringValue(in.Label)
	out.Config = &ChannelConfigModel{
		Type:   types.StringValue(in.Config.Type),
		Format: types.StringValue(in.Config.Format),
		URL:    types.StringValue(in.Config.URL),
	}
}

// ---- CRUD ----

func (r *ChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan ChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := plan.Organization.ValueString()
	prj := plan.Project.ValueString()

	in := channelModelToCreate(&plan)
	out, err := r.client.CreateChannel(ctx, org, prj, in)
	if err != nil {
		resp.Diagnostics.AddError("Create channel failed", err.Error())
		return
	}

	var state ChannelModel
	// preserve org/project from plan
	state.Organization = plan.Organization
	state.Project = plan.Project
	channelReadToModel(out, &state)

	tflog.Trace(ctx, "created channel", map[string]any{"id": state.ID.ValueString(), "org": org, "project": prj})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()

	out, status, err := r.client.GetChannel(ctx, org, prj, state.ID.ValueString())
	if err != nil {
		if status == 404 {
			// Removed out-of-band
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read channel failed", err.Error())
		return
	}

	orgVal := state.Organization
	prjVal := state.Project
	channelReadToModel(out, &state)
	state.Organization = orgVal
	state.Project = prjVal

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan ChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state ChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Missing ID", "Cannot update channel because the current state has no ID.")
		return
	}

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()

	var payload ChannelUpdate
	if !plan.Label.IsNull() && !plan.Label.IsUnknown() {
		v := plan.Label.ValueString()
		payload.Label = &v
	}
	if plan.Config != nil {
		cfg := ChannelConfig{}
		set := false
		if !plan.Config.Type.IsNull() && !plan.Config.Type.IsUnknown() {
			cfg.Type = plan.Config.Type.ValueString()
			set = true
		}
		if !plan.Config.Format.IsNull() && !plan.Config.Format.IsUnknown() {
			cfg.Format = plan.Config.Format.ValueString()
			set = true
		}
		if !plan.Config.URL.IsNull() && !plan.Config.URL.IsUnknown() {
			cfg.URL = plan.Config.URL.ValueString()
			set = true
		}
		if set {
			payload.Config = &cfg
		}
	}

	out, err := r.client.UpdateChannel(ctx, org, prj, state.ID.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update channel failed", err.Error())
		return
	}

	var newState ChannelModel
	newState.Organization = state.Organization
	newState.Project = state.Project
	channelReadToModel(out, &newState)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *ChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()
	prj := state.Project.ValueString()

	if err := r.client.DeleteChannel(ctx, org, prj, state.ID.ValueString()); err != nil {
		// If already gone or server returns something unexpected, treat as successful delete but warn.
		resp.Diagnostics.AddWarning("Delete channel", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *ChannelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Support "organization/project/id" (preferred) or just "id" (legacy).
	parts := strings.Split(req.ID, "/")
	switch len(parts) {
	case 3:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), parts[1])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
	case 1:
		resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	default:
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "organization/project/id" or "id".`,
		)
	}
}
