// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

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

var (
	_ resource.Resource                   = &APIKeyResource{}
	_ resource.ResourceWithConfigure      = &APIKeyResource{}
	_ resource.ResourceWithImportState    = &APIKeyResource{}
	_ resource.ResourceWithValidateConfig = &APIKeyResource{}
)

// NewAPIKeyResource returns the generic unified API key resource.
func NewAPIKeyResource() resource.Resource { return &APIKeyResource{} }

// APIKeyResource manages any Logfire API key through the unified
// /v1/api-keys endpoints. OTLP read/write tokens and gateway keys are all
// API keys with different scopes; use this resource for multi-scope keys,
// org-wide keys, management keys, or anything the typed convenience resources
// do not cover.
type APIKeyResource struct {
	client *logclient.APIClient
}

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

func apiKeyGatewayBlockDescription() string {
	return "Gateway per-scope settings for keys with the `project:gateway_proxy` scope. " +
		"Updatable in place. Removing the block clears every cap the key carries."
}

func gatewaySettingsAttributes() map[string]rschema.Attribute {
	return map[string]rschema.Attribute{
		"spending_limit_daily": rschema.Int64Attribute{
			Optional:            true,
			MarkdownDescription: "Maximum gateway spend in whole US dollars per day. Null means no limit.",
			Validators: []validator.Int64{
				int64validator.Between(0, logclient.GatewaySpendingLimitMaxUSD),
			},
		},
		"spending_limit_weekly": rschema.Int64Attribute{
			Optional:            true,
			MarkdownDescription: "Maximum gateway spend in whole US dollars per week. Null means no limit.",
			Validators: []validator.Int64{
				int64validator.Between(0, logclient.GatewaySpendingLimitMaxUSD),
			},
		},
		"spending_limit_monthly": rschema.Int64Attribute{
			Optional:            true,
			MarkdownDescription: "Maximum gateway spend in whole US dollars per month. Null means no limit.",
			Validators: []validator.Int64{
				int64validator.Between(0, logclient.GatewaySpendingLimitMaxUSD),
			},
		},
		"spending_limit_total": rschema.Int64Attribute{
			Optional:            true,
			MarkdownDescription: "Maximum lifetime gateway spend in whole US dollars. Null means no limit.",
			Validators: []validator.Int64{
				int64validator.Between(0, logclient.GatewaySpendingLimitMaxUSD),
			},
		},
		"cache_enabled": rschema.BoolAttribute{
			Optional:            true,
			MarkdownDescription: "Whether gateway responses are cached. Null inherits the project default.",
		},
	}
}

func (r *APIKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *APIKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire API key. Write tokens (`project:write_otlp`), read tokens " +
			"(`project:read_otlp`), gateway keys (`project:gateway_proxy`), and management keys are all API keys " +
			"with different scopes. The new key can never exceed the provider credential's own grant: its scopes " +
			"must be a subset of the provider `api_key` scopes, so the provider key needs `organization:create_api_key` " +
			"plus every scope it delegates.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "API key identifier. Use this identifier to import an existing key.",
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
				MarkdownDescription: "Optional RFC3339 expiration timestamp (for example `2026-12-31T23:59:59Z`). If omitted, the key does not expire. Changing it replaces the key: expiry cannot be updated in place (an expired key cannot be re-enabled; create a new key instead).",
				Validators: []validator.String{
					newOptionalRFC3339Validator(),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"gateway": rschema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: apiKeyGatewayBlockDescription(),
				Attributes:          gatewaySettingsAttributes(),
			},
			"token": rschema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The plaintext API key. Only returned on creation; never returned again by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the project the key is scoped to, if any.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
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
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_by_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Display name of the user that created the key, when known.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *APIKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	if missing := scopesRequiringProject(scopes); len(missing) > 0 {
		if config.ProjectID.IsNull() && !config.ProjectID.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root("project_id"),
				"Missing project scope",
				fmt.Sprintf("Scopes %q require a specific project_id and cannot be combined with an org-wide key.", missing),
			)
		}
	}
	if !gatewaySettingsIsNull(config.Gateway) && !scopesContain(scopes, GatewayProxyScope) {
		resp.Diagnostics.AddAttributeError(
			path.Root("gateway"),
			"Unexpected gateway settings",
			fmt.Sprintf("The gateway block configures the %q scope, which is not in scopes. Add it or remove the block.", GatewayProxyScope),
		)
	}
}

func apiKeyCreateFromPlan(plan *APIKeyModel) (logclient.APIKeyCreate, error) {
	scopes := sortedScopeStrings(plan.Scopes)
	expiresAt, err := parseOptionalRFC3339(plan.ExpiresAt)
	if err != nil {
		return logclient.APIKeyCreate{}, err
	}
	in := logclient.APIKeyCreate{
		Name:        plan.Name.ValueString(),
		Scopes:      scopes,
		Description: optionalStringPointer(plan.Description),
		Claims:      gatewayClaimsForCreate(plan.Gateway),
		ExpiresAt:   expiresAt,
	}
	if !plan.ProjectID.IsNull() && !plan.ProjectID.IsUnknown() && plan.ProjectID.ValueString() != "" {
		v := plan.ProjectID.ValueString()
		in.ProjectID = &v
	}
	return in, nil
}

// apiKeyReadToState maps a read-side key into state, preserving the
// create-only token from prior state since list/update never return it.
func apiKeyReadToState(read *logclient.APIKeyRead, state *APIKeyModel, priorToken types.String) {
	state.ID = types.StringValue(read.ID)
	state.Name = types.StringValue(read.Name)
	scopes := append([]string(nil), read.Scopes...)
	state.Scopes = scopeSetValue(scopes)
	setOptionalStringFromPointer(&state.ProjectID, read.ProjectID)
	setOptionalStringFromPointer(&state.Description, read.Description)
	setOptionalStringFromPointer(&state.ExpiresAt, read.ExpiresAt)
	state.Gateway = gatewaySettingsFromClaims(read.Claims)
	state.ProjectName = types.StringNull()
	if read.ProjectName != nil && *read.ProjectName != "" {
		state.ProjectName = types.StringValue(*read.ProjectName)
	}
	state.AllProjects = types.BoolValue(read.AllProjects)
	state.Active = types.BoolValue(read.Active)
	if read.CreatedAt != "" {
		state.CreatedAt = types.StringValue(read.CreatedAt)
	} else {
		state.CreatedAt = types.StringNull()
	}
	state.CreatedByName = types.StringNull()
	if read.CreatedByName != nil && *read.CreatedByName != "" {
		state.CreatedByName = types.StringValue(*read.CreatedByName)
	}
	if !priorToken.IsNull() && !priorToken.IsUnknown() {
		state.Token = priorToken
	} else {
		state.Token = types.StringNull()
	}
}

func scopeSetValue(scopes []string) types.Set {
	elements := make([]types.String, 0, len(scopes))
	for _, s := range scopes {
		elements = append(elements, types.StringValue(s))
	}
	set, diags := types.SetValueFrom(context.Background(), types.StringType, elements)
	if diags.HasError() {
		return types.SetNull(types.StringType)
	}
	return set
}

func (r *APIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan APIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, err := apiKeyCreateFromPlan(&plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid expires_at", err.Error())
		return
	}

	out, err := r.client.CreateAPIKey(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create API key failed", err.Error())
		return
	}

	var state APIKeyModel
	apiKeyReadToState(&out.APIKey, &state, types.StringValue(out.Token))
	tflog.Trace(ctx, "created API key", map[string]any{"id": state.ID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *APIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state APIKeyModel
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
		resp.Diagnostics.AddError("Read API key failed", err.Error())
		return
	}

	var newState APIKeyModel
	apiKeyReadToState(read, &newState, state.Token)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *APIKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan APIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state APIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot update API key because the current state has no ID.")
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
	if claims, ok := gatewayClaimsMapForUpdate(plan.Gateway, state.Gateway); ok {
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
			resp.Diagnostics.AddError("Read API key failed", err.Error())
			return
		}
		var newState APIKeyModel
		apiKeyReadToState(read, &newState, state.Token)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		return
	}

	read, err := r.client.UpdateAPIKey(ctx, id, payload)
	if err != nil {
		if logclient.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Update API key failed", err.Error())
		return
	}

	var newState APIKeyModel
	apiKeyReadToState(read, &newState, state.Token)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *APIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state APIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot delete API key because the current state has no ID.")
		return
	}

	if err := r.client.DeleteAPIKey(ctx, state.ID.ValueString()); err != nil {
		if logclient.IsNotFoundError(err) {
			return
		}
		resp.Diagnostics.AddError("Delete API key failed", err.Error())
	}
}

func (r *APIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	parts, err := splitImportParts(req.ID, 1)
	if err != nil || parts[0] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			`Expected the API key UUID. Example: terraform import logfire_api_key.key "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"`,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
}
