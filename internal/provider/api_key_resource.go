// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"slices"

	int64validator "github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	setvalidator "github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// APIKeyResource manages any Logfire API key through the unified
// /v1/api-keys endpoints. OTLP read/write tokens and gateway keys are all
// API keys with different scopes; use this resource for multi-scope keys,
// org-wide keys, management keys, or anything the typed convenience resources
// do not cover.
type APIKeyResource struct{ configured }

// APIKeyModel is the Terraform state for logfire_api_key.
type APIKeyModel struct {
	ID            types.String          `tfsdk:"id"`
	Name          types.String          `tfsdk:"name"`
	Scopes        types.Set             `tfsdk:"scopes"`
	ProjectID     types.String          `tfsdk:"project_id"`
	Description   types.String          `tfsdk:"description"`
	ExpiresAt     types.String          `tfsdk:"expires_at"`
	Gateway       *GatewaySettingsModel `tfsdk:"gateway"`
	Token         types.String          `tfsdk:"token"`
	ProjectName   types.String          `tfsdk:"project_name"`
	AllProjects   types.Bool            `tfsdk:"all_projects"`
	Active        types.Bool            `tfsdk:"active"`
	CreatedAt     types.String          `tfsdk:"created_at"`
	CreatedByName types.String          `tfsdk:"created_by_name"`
}

func spendingLimitAttribute(description string) rschema.Int64Attribute {
	return rschema.Int64Attribute{
		Optional:            true,
		MarkdownDescription: description,
		Validators: []validator.Int64{
			int64validator.Between(0, logclient.GatewaySpendingLimitMaxUSD),
		},
	}
}

func (r *APIKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *APIKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	useState := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire API key. Write tokens (`project:write_otlp`), read tokens (`project:read_otlp`), gateway keys (`project:gateway_proxy`), and management keys are all API keys with different scopes. The new key can never exceed the provider credential's own grant: its scopes must be a subset of the provider `api_key` scopes, so the provider key needs `organization:create_api_key` plus every scope it delegates.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "API key identifier. Use this identifier to import an existing key.",
				PlanModifiers:       useState,
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Display name for the key. Can be updated in place.",
				Validators:          nonEmpty,
			},
			"scopes": rschema.SetAttribute{
				ElementType:         types.StringType,
				Required:            true,
				MarkdownDescription: "OAuth scopes granted to the key (for example `project:write_otlp`). Changing scopes replaces the key: scopes cannot be updated in place.",
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
				},
			},
			"project_id": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "UUID of the project the key is scoped to. Omit for an org-wide key. Changing the project replaces the key. Scopes `project:read_otlp`, `project:write_otlp`, and `project:gateway_proxy` require a project.",
				PlanModifiers:       replace,
				Validators:          nonEmpty,
			},
			"description": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form description. Can be updated in place; setting it to null clears it.",
				Validators:          nonEmpty,
			},
			"expires_at": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional RFC3339 expiration timestamp (for example `2026-12-31T23:59:59Z`). If omitted, the key does not expire. Changing it replaces the key: expiry cannot be updated in place (an expired key cannot be re-enabled; create a new key instead).",
				Validators: []validator.String{
					newOptionalRFC3339Validator(),
				},
				PlanModifiers: replace,
			},
			"gateway": rschema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Gateway per-scope settings for keys with the `project:gateway_proxy` scope. Updatable in place. Removing the block clears every cap the key carries.",
				Attributes: map[string]rschema.Attribute{
					"spending_limit_daily": spendingLimitAttribute(
						"Maximum gateway spend in whole US dollars per day. Null means no limit."),
					"spending_limit_weekly": spendingLimitAttribute(
						"Maximum gateway spend in whole US dollars per week. Null means no limit."),
					"spending_limit_monthly": spendingLimitAttribute(
						"Maximum gateway spend in whole US dollars per month. Null means no limit."),
					"spending_limit_total": spendingLimitAttribute(
						"Maximum lifetime gateway spend in whole US dollars. Null means no limit."),
					"cache_enabled": rschema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Whether gateway responses are cached. Null inherits the project default.",
					},
				},
			},
			"token": rschema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The plaintext API key. Only returned on creation; never returned again by the API.",
				PlanModifiers:       useState,
			},
			"project_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the project the key is scoped to, if any.",
				PlanModifiers:       useState,
			},
			"all_projects": rschema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "True for org-wide keys with no project scope.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
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
			"created_by_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Display name of the user that created the key, when known.",
				PlanModifiers:       useState,
			},
		},
	}
}

// ValidateConfig rejects scope/project combinations the API would refuse, so
// the failure surfaces before apply.
func (r *APIKeyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config APIKeyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Scopes.IsNull() || config.Scopes.IsUnknown() {
		return
	}
	scopes := sortedScopeStrings(config.Scopes)
	missing := scopesRequiringProject(scopes)
	if len(missing) > 0 && config.ProjectID.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("project_id"),
			"Missing project scope",
			fmt.Sprintf("Scopes %q require a specific project_id and cannot "+
				"be combined with an org-wide key.", missing),
		)
	}
	if config.Gateway == nil {
		return
	}
	if !slices.Contains(scopes, GatewayProxyScope) {
		resp.Diagnostics.AddAttributeError(
			path.Root("gateway"),
			"Unexpected gateway settings",
			fmt.Sprintf("The gateway block configures the %q scope, which is "+
				"not in scopes. Add it or remove the block.", GatewayProxyScope),
		)
	}
	// An empty block carries no settings and reads back as null, which would
	// diff forever against the configured empty object. Unknown fields skip
	// this check: gatewaySettingsIsEmpty is false while any field is unknown.
	if gatewaySettingsIsEmpty(config.Gateway) {
		resp.Diagnostics.AddAttributeError(
			path.Root("gateway"),
			"Empty gateway settings",
			"Set at least one spending limit or cache_enabled, or remove the gateway block.",
		)
	}
}

func apiKeyCreateFromPlan(plan *APIKeyModel) logclient.APIKeyCreate {
	return logclient.APIKeyCreate{
		Name:        plan.Name.ValueString(),
		Scopes:      sortedScopeStrings(plan.Scopes),
		Description: optionalStringPointer(plan.Description),
		Claims:      gatewayClaimsForCreate(plan.Gateway),
		ExpiresAt:   expiresAtPointer(plan.ExpiresAt),
		ProjectID:   optionalStringPointer(plan.ProjectID),
	}
}

// apiKeyToModel maps a read-side key into state. The create-only token comes
// from prior state, since list and update never return it.
func apiKeyToModel(read *logclient.APIKeyRead, token types.String) APIKeyModel {
	scopes, _ := types.SetValueFrom(context.Background(), types.StringType, read.Scopes)
	model := APIKeyModel{
		ID:            types.StringValue(read.ID),
		Name:          types.StringValue(read.Name),
		Scopes:        scopes,
		ProjectID:     stringOrNull(read.ProjectID),
		Description:   stringOrNull(read.Description),
		ExpiresAt:     stringOrNull(read.ExpiresAt),
		Token:         token,
		ProjectName:   stringOrNull(read.ProjectName),
		AllProjects:   types.BoolValue(read.AllProjects),
		Active:        types.BoolValue(read.Active),
		CreatedAt:     stringOrNull(&read.CreatedAt),
		CreatedByName: stringOrNull(read.CreatedByName),
	}
	if read.Claims.GatewayProxy != nil {
		gateway := gatewaySettingsFromClaims(read.Claims.GatewayProxy)
		model.Gateway = &gateway
	}
	return model
}

func (r *APIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var plan APIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateAPIKey(ctx, apiKeyCreateFromPlan(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Create API key failed", err.Error())
		return
	}

	state := apiKeyToModel(&out.APIKey, types.StringValue(out.Token))
	tflog.Trace(ctx, "created API key", map[string]any{"id": state.ID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *APIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var state APIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	read, err := r.client.GetAPIKey(ctx, state.ID.ValueString())
	read = apiKeyResult(ctx, read, err, "Read", "API key", &resp.State, &resp.Diagnostics)
	if read != nil {
		newState := apiKeyToModel(read, state.Token)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
	}
}

func (r *APIKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state APIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	read := updateAPIKey(ctx, r.client, &plan, &state, "API key", resp)
	if read != nil {
		newState := apiKeyToModel(read, state.Token)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
	}
}

func (r *APIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	var state APIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteAPIKey(ctx, state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Delete API key failed", err.Error())
	}
}

func (r *APIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if rejectOrganizationContext(r.client, &resp.Diagnostics) {
		return
	}
	parts, err := splitImportParts(req.ID, 1)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			`Expected the API key UUID. Example: terraform import `+
				`logfire_api_key.key "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"`,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
}
