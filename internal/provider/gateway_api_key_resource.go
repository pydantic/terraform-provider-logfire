// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"slices"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// GatewayAPIKeyResource manages a project-scoped AI Gateway API key. It is a
// convenience wrapper over the unified API-keys endpoint with scopes fixed to
// [project:gateway_proxy]: the spend caps and cache toggle below are stored as
// that scope's claims. For multi-scope or org-wide keys, use logfire_api_key.
type GatewayAPIKeyResource struct{ configured }

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

func (r *GatewayAPIKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_api_key"
}

func (r *GatewayAPIKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	useState := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	limit := func(window string) rschema.Int64Attribute {
		return spendingLimitAttribute(fmt.Sprintf("Maximum gateway spend in "+
			"whole US dollars per %s. Null means no limit. Can be updated in "+
			"place; setting it to null clears it.", window))
	}
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a project-scoped AI Gateway API key (`project:gateway_proxy` scope). Do not confuse this with `logfire_gateway_provider`, which configures an upstream LLM provider credential. The provider credential needs `organization:create_api_key` plus `project:gateway_proxy` delegation; a project-scoped operator key for this is minted from the organization-level API keys page with the project selected.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Gateway API key identifier. Use this identifier to import an existing key.",
				PlanModifiers:       useState,
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Display name for the key. Can be updated in place.",
				Validators:          nonEmpty,
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the project the key proxies for. Gateway keys always require a project. Changing it replaces the key.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: nonEmpty,
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form description. Can be updated in place; setting it to null clears it.",
				Validators:          nonEmpty,
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
			"spending_limit_daily":   limit("day"),
			"spending_limit_weekly":  limit("week"),
			"spending_limit_monthly": limit("month"),
			"spending_limit_total":   limit("lifetime"),
			"cache_enabled": rschema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether gateway responses are cached. Null inherits the project default. Can be updated in place; setting it to null restores the default.",
			},
			"token": rschema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The plaintext gateway API key. Only returned on creation; never returned again by the API.",
				PlanModifiers:       useState,
			},
			"project_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the project the key proxies for.",
				PlanModifiers:       useState,
			},
			"active": rschema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "False once the key is disabled. Disable/enable is managed outside Terraform; deleting the resource revokes the key.",
			},
			"created_at": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the key was created.",
				PlanModifiers:       useState,
			},
		},
	}
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

// gatewayAPIKeyView is the part of a gateway key that the generic API key
// update logic acts on.
func gatewayAPIKeyView(m *GatewayAPIKeyModel) *APIKeyModel {
	return &APIKeyModel{
		ID:          m.ID,
		Name:        m.Name,
		Description: m.Description,
		Gateway:     gatewayAPIKeySettings(m),
	}
}

// gatewayAPIKeyToModel maps a read-side key into state, preserving the
// create-only token. It fails when the key does not carry the gateway-proxy
// scope, since this resource cannot manage it.
func gatewayAPIKeyToModel(read *logclient.APIKeyRead, token types.String) (GatewayAPIKeyModel, error) {
	if !slices.Contains(read.Scopes, GatewayProxyScope) {
		return GatewayAPIKeyModel{}, fmt.Errorf("API key %q does not carry "+
			"the %q scope and cannot be managed by logfire_gateway_api_key; "+
			"use logfire_api_key instead", read.ID, GatewayProxyScope)
	}
	key := apiKeyToModel(read, token)
	var settings GatewaySettingsModel
	if key.Gateway != nil {
		settings = *key.Gateway
	}
	return GatewayAPIKeyModel{
		ID:                   key.ID,
		Name:                 key.Name,
		ProjectID:            key.ProjectID,
		Description:          key.Description,
		ExpiresAt:            key.ExpiresAt,
		SpendingLimitDaily:   settings.SpendingLimitDaily,
		SpendingLimitWeekly:  settings.SpendingLimitWeekly,
		SpendingLimitMonthly: settings.SpendingLimitMonthly,
		SpendingLimitTotal:   settings.SpendingLimitTotal,
		CacheEnabled:         settings.CacheEnabled,
		Token:                key.Token,
		ProjectName:          key.ProjectName,
		Active:               key.Active,
		CreatedAt:            key.CreatedAt,
	}, nil
}

// storeGatewayAPIKey stores a key read back for the given operation, which
// fails for a key that is not a gateway key.
func storeGatewayAPIKey(ctx context.Context, read *logclient.APIKeyRead, token types.String, verb string, state *tfsdk.State, diags *diag.Diagnostics) {
	model, err := gatewayAPIKeyToModel(read, token)
	if err != nil {
		diags.AddError(verb+" gateway API key failed", err.Error())
		return
	}
	diags.Append(state.Set(ctx, &model)...)
}

func (r *GatewayAPIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var plan GatewayAPIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateAPIKey(ctx, logclient.APIKeyCreate{
		Name:        plan.Name.ValueString(),
		Scopes:      []string{GatewayProxyScope},
		Description: optionalStringPointer(plan.Description),
		Claims:      gatewayClaimsForCreate(gatewayAPIKeySettings(&plan)),
		ProjectID:   plan.ProjectID.ValueStringPointer(),
		ExpiresAt:   expiresAtPointer(plan.ExpiresAt),
	})
	if err != nil {
		resp.Diagnostics.AddError("Create gateway API key failed", err.Error())
		return
	}

	tflog.Trace(ctx, "created gateway API key", map[string]any{"id": out.APIKey.ID})
	storeGatewayAPIKey(ctx, &out.APIKey, types.StringValue(out.Token), "Create",
		&resp.State, &resp.Diagnostics)
}

func (r *GatewayAPIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var state GatewayAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	read, err := r.client.GetAPIKey(ctx, state.ID.ValueString())
	read = apiKeyResult(ctx, read, err, "Read", "gateway API key", &resp.State,
		&resp.Diagnostics)
	if read != nil {
		storeGatewayAPIKey(ctx, read, state.Token, "Read", &resp.State,
			&resp.Diagnostics)
	}
}

func (r *GatewayAPIKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state GatewayAPIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	read := updateAPIKey(ctx, r.client, gatewayAPIKeyView(&plan),
		gatewayAPIKeyView(&state), "gateway API key", resp)
	if read != nil {
		storeGatewayAPIKey(ctx, read, state.Token, "Update", &resp.State,
			&resp.Diagnostics)
	}
}

func (r *GatewayAPIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var state GatewayAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteAPIKey(ctx, state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Delete gateway API key failed", err.Error())
	}
}

func (r *GatewayAPIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	parts, err := splitImportParts(req.ID, 1)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			`Expected the gateway API key UUID. Example: terraform import `+
				`logfire_gateway_api_key.key `+
				`"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"`,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
}
