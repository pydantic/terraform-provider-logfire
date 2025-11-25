// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ resource.Resource = &ChannelResource{}
var _ resource.ResourceWithConfigure = &ChannelResource{}
var _ resource.ResourceWithImportState = &ChannelResource{}

func NewChannelResource() resource.Resource { return &ChannelResource{} }

type ChannelResource struct {
	client *logclient.APIClient
}

type ChannelConfigModel struct {
	// Email config exists in the API but is not exposed via Terraform yet.
	Type    types.String `tfsdk:"type"` // webhook | opsgenie
	Format  types.String `tfsdk:"format"`
	URL     types.String `tfsdk:"url"`
	AuthKey types.String `tfsdk:"auth_key"`
}

type ChannelModel struct {
	ID     types.String        `tfsdk:"id"`
	Name   types.String        `tfsdk:"name"`
	Active types.Bool          `tfsdk:"active"`
	Config *ChannelConfigModel `tfsdk:"config"`
}

func (r *ChannelResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_channel"
}

func (r *ChannelResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire alert channel.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Channel ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Channel name.",
			},
			"active": rschema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the channel is active.",
				Default:             booldefault.StaticBool(true),
			},
		},
		Blocks: map[string]rschema.Block{
			"config": rschema.SingleNestedBlock{
				MarkdownDescription: "Channel configuration.",
				Attributes: map[string]rschema.Attribute{
					"type": rschema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Channel type (`webhook` or `opsgenie`).",
						Validators: []validator.String{
							stringvalidator.OneOf("webhook", "opsgenie"),
						},
					},
					"format": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Webhook payload format.",
						Validators: []validator.String{
							stringvalidator.OneOf("auto", "slack-blockkit", "slack-legacy", "raw-data"),
						},
					},
					"url": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Webhook URL endpoint.",
					},
					"auth_key": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Opsgenie API key.",
					},
				},
			},
		},
	}
}

func (r *ChannelResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func channelModelToCreate(m *ChannelModel) (logclient.ChannelCreate, diag.Diagnostics) {
	cfg, diags := channelConfigModelToAPI(m.Config)
	if diags.HasError() {
		return logclient.ChannelCreate{}, diags
	}
	return logclient.ChannelCreate{
		Label:  m.Name.ValueString(),
		Config: cfg,
	}, nil
}

func channelConfigModelToAPI(m *ChannelConfigModel) (interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m == nil {
		diags.Append(diag.NewErrorDiagnostic("Invalid channel config", "config block must be provided."))
		return nil, diags
	}
	if m.Type.IsNull() || m.Type.IsUnknown() {
		diags.Append(diag.NewErrorDiagnostic("Invalid channel config", "config.type must be set."))
		return nil, diags
	}

	cfgType := m.Type.ValueString()

	switch cfgType {
	case "webhook":
		format, fDiags := requiredConfigString(m.Format, "config.format", "webhook")
		diags.Append(fDiags...)
		url, uDiags := requiredConfigString(m.URL, "config.url", "webhook")
		diags.Append(uDiags...)
		diags.Append(disallowConfigString(m.AuthKey, "config.auth_key", "webhook")...)
		if diags.HasError() {
			return nil, diags
		}
		return &logclient.WebhookConfig{
			ChannelConfigBase: logclient.ChannelConfigBase{Type: "webhook"},
			Format:            stringPointer(format),
			URL:               stringPointer(url),
		}, diags
	case "opsgenie":
		key, kDiags := requiredConfigString(m.AuthKey, "config.auth_key", "opsgenie")
		diags.Append(kDiags...)
		diags.Append(disallowConfigString(m.Format, "config.format", "opsgenie")...)
		diags.Append(disallowConfigString(m.URL, "config.url", "opsgenie")...)
		if diags.HasError() {
			return nil, diags
		}
		return &logclient.OpsgenieConfig{
			ChannelConfigBase: logclient.ChannelConfigBase{Type: "opsgenie"},
			AuthKey:           stringPointer(key),
		}, diags
	case "email":
		// Email config exists in API but not exposed in Terraform yet
		diags.Append(diag.NewErrorDiagnostic("Invalid channel config", "email channels are not yet supported via Terraform."))
		return nil, diags
	default:
		diags.Append(diag.NewErrorDiagnostic("Invalid channel config", fmt.Sprintf("unsupported config type %q", cfgType)))
		return nil, diags
	}
}

func channelConfigAPIToModel(cfg interface{}) (*ChannelConfigModel, diag.Diagnostics) {
	// Convert to JSON and back to generic ChannelConfig to determine the type
	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, diag.Diagnostics{
			diag.NewErrorDiagnostic("Invalid channel config", fmt.Sprintf("failed to marshal config: %v", err)),
		}
	}

	var genericCfg logclient.ChannelConfig
	if err := json.Unmarshal(jsonBytes, &genericCfg); err != nil {
		return nil, diag.Diagnostics{
			diag.NewErrorDiagnostic("Invalid channel config", fmt.Sprintf("failed to unmarshal config: %v", err)),
		}
	}

	model := &ChannelConfigModel{
		Type:    types.StringValue(genericCfg.Type),
		Format:  types.StringNull(),
		URL:     types.StringNull(),
		AuthKey: types.StringNull(),
	}

	switch genericCfg.Type {
	case "webhook":
		// Type assert to WebhookConfig
		if webhookCfg, ok := cfg.(*logclient.WebhookConfig); ok {
			if webhookCfg.Format != nil {
				model.Format = types.StringValue(*webhookCfg.Format)
			}
			if webhookCfg.URL != nil {
				model.URL = types.StringValue(*webhookCfg.URL)
			}
		} else {
			// Fallback to generic fields
			if genericCfg.Format != nil {
				model.Format = types.StringValue(*genericCfg.Format)
			}
			if genericCfg.URL != nil {
				model.URL = types.StringValue(*genericCfg.URL)
			}
		}
	case "opsgenie":
		// Type assert to OpsgenieConfig
		if opsgenieCfg, ok := cfg.(*logclient.OpsgenieConfig); ok {
			if opsgenieCfg.AuthKey != nil {
				model.AuthKey = types.StringValue(*opsgenieCfg.AuthKey)
			}
		} else {
			// Fallback to generic fields
			if genericCfg.AuthKey != nil {
				model.AuthKey = types.StringValue(*genericCfg.AuthKey)
			}
		}
	case "email":
		// Email channels exist in API but not exposed in Terraform yet
		return nil, diag.Diagnostics{
			diag.NewErrorDiagnostic("Unsupported channel config", "email channels are not yet supported via Terraform."),
		}
	default:
		return nil, diag.Diagnostics{
			diag.NewErrorDiagnostic("Invalid channel config", fmt.Sprintf("unsupported config type %q", genericCfg.Type)),
		}
	}
	return model, nil
}

func requiredConfigString(val types.String, fieldName, channelType string) (string, diag.Diagnostics) {
	if val.IsNull() || val.IsUnknown() {
		return "", diag.Diagnostics{
			diag.NewErrorDiagnostic("Invalid channel config", fmt.Sprintf("%s must be set for %s channels.", fieldName, channelType)),
		}
	}
	v := strings.TrimSpace(val.ValueString())
	if v == "" {
		return "", diag.Diagnostics{
			diag.NewErrorDiagnostic("Invalid channel config", fmt.Sprintf("%s cannot be empty for %s channels.", fieldName, channelType)),
		}
	}
	return v, nil
}

func stringPointer(v string) *string {
	return &v
}

func disallowConfigString(val types.String, fieldName, channelType string) diag.Diagnostics {
	if val.IsNull() || val.IsUnknown() {
		return nil
	}
	if strings.TrimSpace(val.ValueString()) == "" {
		return nil
	}
	return diag.Diagnostics{
		diag.NewErrorDiagnostic("Invalid channel config", fmt.Sprintf("%s cannot be set for %s channels.", fieldName, channelType)),
	}
}

func channelReadToModel(c *logclient.ChannelRead, m *ChannelModel) diag.Diagnostics {
	m.ID = types.StringValue(c.ID)
	m.Name = types.StringValue(c.Label)
	m.Active = types.BoolValue(c.Active)
	cfg, diags := channelConfigAPIToModel(c.Config)
	if diags.HasError() {
		return diags
	}
	m.Config = cfg
	return nil
}

// --- CRUD ---

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

	in, diags := channelModelToCreate(&plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateChannel(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create channel failed", err.Error())
		return
	}

	if !plan.Active.IsNull() && !plan.Active.IsUnknown() {
		desired := plan.Active.ValueBool()
		if desired != out.Active {
			payload := logclient.ChannelUpdate{Active: logclient.NullableFieldValue(desired)}
			updated, uerr := r.client.UpdateChannel(ctx, out.ID, payload)
			if uerr != nil {
				resp.Diagnostics.AddError("Create channel failed", fmt.Sprintf("setting active flag: %v", uerr))
				return
			}
			out = updated
		}
	}

	var state ChannelModel
	if diags := channelReadToModel(out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	tflog.Trace(ctx, "created channel", map[string]any{"id": state.ID.ValueString()})
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

	id := state.ID.ValueString()

	out, status, err := r.client.GetChannel(ctx, id)
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read channel failed", err.Error())
		return
	}

	if diags := channelReadToModel(out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

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

	id := state.ID.ValueString()

	var payload logclient.ChannelUpdate
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		v := plan.Name.ValueString()
		payload.Label = logclient.NullableFieldValue(v)
	}
	if !plan.Active.IsNull() && !plan.Active.IsUnknown() {
		v := plan.Active.ValueBool()
		if state.Active.IsNull() || state.Active.IsUnknown() || state.Active.ValueBool() != v {
			payload.Active = logclient.NullableFieldValue(v)
		}
	}
	if plan.Config != nil {
		cfg, diags := channelConfigModelToAPI(plan.Config)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		payload.Config = &cfg
	}

	out, err := r.client.UpdateChannel(ctx, id, payload)
	if err != nil {
		resp.Diagnostics.AddError("Update channel failed", err.Error())
		return
	}

	var newState ChannelModel
	if diags := channelReadToModel(out, &newState); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
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

	id := state.ID.ValueString()

	if err := r.client.DeleteChannel(ctx, id); err != nil {
		if logclient.IsRateLimitError(err) {
			resp.Diagnostics.AddError("Delete channel", fmt.Sprintf("rate limited while deleting channel: %v", err))
			return
		}
		// If already gone, treat as successful delete but log warning
		resp.Diagnostics.AddWarning("Delete channel", fmt.Sprintf("delete returned error: %v", err))
	}
}

func (r *ChannelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use: terraform import logfire_channel.prod "<channel_id>"`,
		)
		return
	}

	parts := strings.Split(req.ID, "/")
	id := parts[len(parts)-1]
	if id == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			`Expected "<channel_id>".`,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
