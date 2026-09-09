// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	int64validator "github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

var _ resource.Resource = &GatewayAPIKeyResource{}
var _ resource.ResourceWithConfigure = &GatewayAPIKeyResource{}
var _ resource.ResourceWithImportState = &GatewayAPIKeyResource{}

// NewGatewayAPIKeyResource returns the typed gateway API key resource.
func NewGatewayAPIKeyResource() resource.Resource { return &GatewayAPIKeyResource{} }

// GatewayAPIKeyResource manages a project-scoped AI Gateway API key. It is a
// convenience wrapper over the unified API-keys endpoint with scopes fixed to
// [project:gateway_proxy]: the spend caps and cache toggle below are stored as
// that scope's claims. For multi-scope or org-wide keys, use logfire_api_key.
type GatewayAPIKeyResource struct {
	client *logclient.APIClient
}

// GatewayAPIKeyModel is the Terraform state for logfire_gateway_api_key.
type GatewayAPIKeyModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	ProjectID            types.String `tfsdk:"project_id"`
	Description          types.String `tfsdk:"description"`
	ExpiresAt            types.String `tfsdk:"expires_at"`
	SpendingLimitDaily   types.Int64  `tfsdk:"spending_limit_daily"`
	SpendingLimitWeekly  types.Int64  `tfsdk:"spending_limit_weekly"`
	SpendingLimitMonthly types.Int64  `tfsdk:"spending_limit_monthly"`
	SpendingLimitTotal   types.Int64  `tfsdk:"spending_limit_total"`
	CacheEnabled         types.Bool   `tfsdk:"cache_enabled"`
	Token                types.String `tfsdk:"token"`
	ProjectName          types.String `tfsdk:"project_name"`
	Active               types.Bool   `tfsdk:"active"`
	CreatedAt            types.String `tfsdk:"created_at"`
}

func gatewayAPIKeySpendingLimitDescription(window string) string {
	return fmt.Sprintf("Maximum gateway spend in whole US dollars per %s. Null means no limit. Can be updated in place; setting it to null clears it.", window)
}

func (r *GatewayAPIKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_api_key"
}

func (r *GatewayAPIKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	spendingLimitValidators := []validator.Int64{
		int64validator.Between(0, logclient.GatewaySpendingLimitMaxUSD),
	}
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a project-scoped AI Gateway API key (`project:gateway_proxy` scope). " +
			"Do not confuse this with `logfire_gateway_provider`, which configures an upstream LLM provider credential. " +
			"The provider credential needs `organization:create_api_key` plus `project:gateway_proxy` delegation.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Gateway API key identifier. Use this identifier to import an existing key.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Display name for the key. Can be updated in place.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the project the key proxies for. Gateway keys always require a project. Changing it replaces the key.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form description. Can be updated in place; setting it to null clears it.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"expires_at": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional RFC3339 expiration timestamp (for example `2026-12-31T23:59:59Z`). If omitted, the key does not expire. Changing it replaces the key.",
				Validators: []validator.String{
					newOptionalRFC3339Validator(),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"spending_limit_daily": rschema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: gatewayAPIKeySpendingLimitDescription("day"),
				Validators:          spendingLimitValidators,
			},
			"spending_limit_weekly": rschema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: gatewayAPIKeySpendingLimitDescription("week"),
				Validators:          spendingLimitValidators,
			},
			"spending_limit_monthly": rschema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: gatewayAPIKeySpendingLimitDescription("month"),
				Validators:          spendingLimitValidators,
			},
			"spending_limit_total": rschema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: gatewayAPIKeySpendingLimitDescription("lifetime"),
				Validators:          spendingLimitValidators,
			},
			"cache_enabled": rschema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether gateway responses are cached. Null inherits the project default. Can be updated in place; setting it to null restores the default.",
			},
			"token": rschema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The plaintext gateway API key. Only returned on creation; never returned again by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the project the key proxies for.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"active": rschema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "False once the key is disabled. Disable/enable is managed outside Terraform; deleting the resource revokes the key.",
			},
			"created_at": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the key was created.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *GatewayAPIKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// gatewayAPIKeySettings projects the flat spend/cache fields into the shared
// gateway settings model for claims helpers.
func gatewayAPIKeySettings(m *GatewayAPIKeyModel) *GatewaySettingsModel {
	return &GatewaySettingsModel{
		SpendingLimitDaily:   m.SpendingLimitDaily,
		SpendingLimitWeekly:  m.SpendingLimitWeekly,
		SpendingLimitMonthly: m.SpendingLimitMonthly,
		SpendingLimitTotal:   m.SpendingLimitTotal,
		CacheEnabled:         m.CacheEnabled,
	}
}

func gatewayAPIKeyCreateFromPlan(plan *GatewayAPIKeyModel) (logclient.APIKeyCreate, error) {
	expiresAt, err := parseOptionalRFC3339(plan.ExpiresAt)
	if err != nil {
		return logclient.APIKeyCreate{}, err
	}
	projectID := plan.ProjectID.ValueString()
	return logclient.APIKeyCreate{
		Name:        plan.Name.ValueString(),
		Scopes:      []string{GatewayProxyScope},
		Description: optionalStringPointer(plan.Description),
		Claims:      gatewayClaimsForCreate(gatewayAPIKeySettings(plan)),
		ProjectID:   &projectID,
		ExpiresAt:   expiresAt,
	}, nil
}

// gatewayAPIKeyReadToState maps a read-side key into state, preserving the
// create-only token. It reports an error diagnostic when the key does not
// carry the gateway-proxy scope, since this resource cannot manage it.
func gatewayAPIKeyReadToState(read *logclient.APIKeyRead, state *GatewayAPIKeyModel, priorToken types.String) error {
	if !scopesContain(read.Scopes, GatewayProxyScope) {
		return fmt.Errorf("API key %q does not carry the %q scope and cannot be managed by logfire_gateway_api_key; use logfire_api_key instead", read.ID, GatewayProxyScope)
	}
	state.ID = types.StringValue(read.ID)
	state.Name = types.StringValue(read.Name)
	if read.ProjectID != nil && *read.ProjectID != "" {
		state.ProjectID = types.StringValue(*read.ProjectID)
	} else {
		state.ProjectID = types.StringNull()
	}
	setOptionalStringFromPointer(&state.Description, read.Description)
	setOptionalStringFromPointer(&state.ExpiresAt, read.ExpiresAt)
	settings := gatewaySettingsFromClaims(read.Claims)
	state.SpendingLimitDaily = types.Int64Null()
	state.SpendingLimitWeekly = types.Int64Null()
	state.SpendingLimitMonthly = types.Int64Null()
	state.SpendingLimitTotal = types.Int64Null()
	state.CacheEnabled = types.BoolNull()
	if settings != nil {
		state.SpendingLimitDaily = settings.SpendingLimitDaily
		state.SpendingLimitWeekly = settings.SpendingLimitWeekly
		state.SpendingLimitMonthly = settings.SpendingLimitMonthly
		state.SpendingLimitTotal = settings.SpendingLimitTotal
		state.CacheEnabled = settings.CacheEnabled
	}
	state.ProjectName = types.StringNull()
	if read.ProjectName != nil && *read.ProjectName != "" {
		state.ProjectName = types.StringValue(*read.ProjectName)
	}
	state.Active = types.BoolValue(read.Active)
	if read.CreatedAt != "" {
		state.CreatedAt = types.StringValue(read.CreatedAt)
	} else {
		state.CreatedAt = types.StringNull()
	}
	if !priorToken.IsNull() && !priorToken.IsUnknown() {
		state.Token = priorToken
	} else {
		state.Token = types.StringNull()
	}
	return nil
}

func (r *GatewayAPIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan GatewayAPIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.ProjectID.IsNull() || plan.ProjectID.IsUnknown() || plan.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "The gateway API key requires a project_id.")
		return
	}

	in, err := gatewayAPIKeyCreateFromPlan(&plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid expires_at", err.Error())
		return
	}

	out, err := r.client.CreateAPIKey(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create gateway API key failed", err.Error())
		return
	}

	var state GatewayAPIKeyModel
	if err := gatewayAPIKeyReadToState(&out.APIKey, &state, types.StringValue(out.Token)); err != nil {
		resp.Diagnostics.AddError("Create gateway API key failed", err.Error())
		return
	}
	// Preserve configured nulls for caps the API echoes back identically; the
	// read mapping above already yields nulls for absent caps.
	tflog.Trace(ctx, "created gateway API key", map[string]any{"id": state.ID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *GatewayAPIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state GatewayAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	read, err := r.client.GetAPIKey(ctx, state.ID.ValueString())
	if err != nil {
		if logclient.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read gateway API key failed", err.Error())
		return
	}

	var newState GatewayAPIKeyModel
	if err := gatewayAPIKeyReadToState(read, &newState, state.Token); err != nil {
		resp.Diagnostics.AddError("Read gateway API key failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *GatewayAPIKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan GatewayAPIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state GatewayAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot update gateway API key because the current state has no ID.")
		return
	}

	var payload logclient.APIKeyUpdate
	sendsUpdate := false
	if !plan.Name.IsUnknown() && (state.Name.IsNull() || state.Name.IsUnknown() || plan.Name.ValueString() != state.Name.ValueString()) {
		v := plan.Name.ValueString()
		payload.Name = logclient.NullableFieldValue(v)
		sendsUpdate = true
	}
	if !plan.Description.IsUnknown() {
		switch {
		case plan.Description.IsNull():
			if !state.Description.IsNull() && !state.Description.IsUnknown() {
				payload.Description = logclient.NullableFieldNull[string]()
				sendsUpdate = true
			}
		default:
			v := plan.Description.ValueString()
			if state.Description.IsNull() || state.Description.IsUnknown() || state.Description.ValueString() != v {
				payload.Description = logclient.NullableFieldValue(v)
				sendsUpdate = true
			}
		}
	}
	if claims, ok := gatewayClaimsMapForUpdate(gatewayAPIKeySettings(&plan), gatewayAPIKeySettings(&state)); ok {
		payload.Claims = &claims
		sendsUpdate = true
	}

	id := state.ID.ValueString()
	if !sendsUpdate {
		read, err := r.client.GetAPIKey(ctx, id)
		if err != nil {
			if logclient.IsNotFoundError(err) {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Read gateway API key failed", err.Error())
			return
		}
		var newState GatewayAPIKeyModel
		if err := gatewayAPIKeyReadToState(read, &newState, state.Token); err != nil {
			resp.Diagnostics.AddError("Read gateway API key failed", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		return
	}

	read, err := r.client.UpdateAPIKey(ctx, id, payload)
	if err != nil {
		if logclient.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Update gateway API key failed", err.Error())
		return
	}

	var newState GatewayAPIKeyModel
	if err := gatewayAPIKeyReadToState(read, &newState, state.Token); err != nil {
		resp.Diagnostics.AddError("Update gateway API key failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *GatewayAPIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state GatewayAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot delete gateway API key because the current state has no ID.")
		return
	}

	if err := r.client.DeleteAPIKey(ctx, state.ID.ValueString()); err != nil {
		if logclient.IsNotFoundError(err) {
			return
		}
		resp.Diagnostics.AddError("Delete gateway API key failed", err.Error())
	}
}

func (r *GatewayAPIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	parts, err := splitImportParts(req.ID, 1)
	if err != nil || parts[0] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			`Expected the gateway API key UUID. Example: terraform import logfire_gateway_api_key.key "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"`,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
}
