// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ resource.Resource = &FrontendApplicationTokenResource{}
var _ resource.ResourceWithConfigure = &FrontendApplicationTokenResource{}
var _ resource.ResourceWithImportState = &FrontendApplicationTokenResource{}

func NewFrontendApplicationTokenResource() resource.Resource {
	return &FrontendApplicationTokenResource{}
}

type FrontendApplicationTokenResource struct{ client *logclient.APIClient }

type FrontendApplicationTokenModel struct {
	ID              types.String `tfsdk:"id"`
	ProjectID       types.String `tfsdk:"project_id"`
	ApplicationID   types.String `tfsdk:"application_id"`
	AdoptTokenID    types.String `tfsdk:"adopt_token_id"`
	RevokeOnDestroy types.Bool   `tfsdk:"revoke_on_destroy"`
	Token           types.String `tfsdk:"token"`
	Status          types.String `tfsdk:"status"`
	CreatedAt       types.String `tfsdk:"created_at"`
	LastUsedAt      types.String `tfsdk:"last_used_at"`
}

func (r *FrontendApplicationTokenResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_frontend_application_token"
}

func (r *FrontendApplicationTokenResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages one active token for a Logfire frontend application. Rotate in two applies with two resource blocks: add the replacement and deploy its `token`, then remove the old resource to revoke it. The provider never revokes an old token while creating a new one. Before destroying the whole application tree, set the last token's `revoke_on_destroy` to `false` in a separate apply; application deletion then revokes every attached token.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true, MarkdownDescription: "Frontend application token identifier.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id":        schema.StringAttribute{Required: true, MarkdownDescription: "UUID of the project that owns the application.", PlanModifiers: replace, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"application_id":    schema.StringAttribute{Required: true, MarkdownDescription: "UUID of the frontend application.", PlanModifiers: replace, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"adopt_token_id":    schema.StringAttribute{Optional: true, MarkdownDescription: "Existing active token to adopt instead of issuing another. Use the application's `token_id` for the initial handoff, then remove this resource only after a separately managed replacement has been deployed.", PlanModifiers: replace, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"revoke_on_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Revoke this token when its resource is destroyed. Before destroying the entire application tree, set this to `false` in a separate apply for the last token so application deletion can revoke all attached tokens."},
			"token":             schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Active restricted browser token. The provider recovers this value on refresh and import.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"status":            schema.StringAttribute{Computed: true, MarkdownDescription: "Token status. A managed token remains in state only while active."},
			"created_at":        schema.StringAttribute{Computed: true, MarkdownDescription: "Timestamp when the token was created.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"last_used_at":      schema.StringAttribute{Computed: true, MarkdownDescription: "Timestamp when the token was last used, when known."},
		},
	}
}

func (r *FrontendApplicationTokenResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func frontendTokenToModel(token *logclient.FrontendApplicationToken, state *FrontendApplicationTokenModel) {
	state.ID = types.StringValue(token.ID)
	state.Status = types.StringValue(token.Status)
	state.CreatedAt = types.StringValue(token.CreatedAt)
	setOptionalString(&state.LastUsedAt, token.LastUsedAt)
	setOptionalString(&state.Token, token.Token)
}

func (r *FrontendApplicationTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	var plan FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state := FrontendApplicationTokenModel{ProjectID: plan.ProjectID, ApplicationID: plan.ApplicationID, AdoptTokenID: plan.AdoptTokenID, RevokeOnDestroy: plan.RevokeOnDestroy}
	tokenID := ""
	var created *logclient.FrontendApplicationTokenCreated
	if !plan.AdoptTokenID.IsNull() && !plan.AdoptTokenID.IsUnknown() {
		tokenID = plan.AdoptTokenID.ValueString()
	} else {
		out, err := r.client.CreateFrontendApplicationToken(ctx, plan.ProjectID.ValueString(), plan.ApplicationID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Create frontend application token failed", err.Error())
			return
		}
		tokenID = out.TokenID
		created = out
	}
	if created != nil {
		state.ID = types.StringValue(tokenID)
		state.Status = types.StringValue(created.Status)
		state.Token = types.StringValue(created.Token)
		state.CreatedAt = types.StringValue(created.CreatedAt)
		setOptionalString(&state.LastUsedAt, created.LastUsedAt)
	} else {
		tokens, err := r.client.ListFrontendApplicationTokens(ctx, plan.ProjectID.ValueString(), plan.ApplicationID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Read adopted frontend application token failed", err.Error())
			return
		}
		found := false
		for i := range tokens {
			if tokens[i].ID == tokenID && tokens[i].Status == "active" && tokens[i].Token != nil {
				frontendTokenToModel(&tokens[i], &state)
				found = true
				break
			}
		}
		if !found {
			resp.Diagnostics.AddError("Read frontend application token failed", "The API did not return the selected token as an active token for this application.")
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FrontendApplicationTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	var state FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tokens, err := r.client.ListFrontendApplicationTokens(ctx, state.ProjectID.ValueString(), state.ApplicationID.ValueString())
	if err != nil {
		if logclient.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read frontend application token failed", err.Error())
		return
	}
	for i := range tokens {
		if tokens[i].ID != state.ID.ValueString() {
			continue
		}
		if tokens[i].Status != "active" || tokens[i].Token == nil {
			resp.State.RemoveResource(ctx)
			return
		}
		var refreshed FrontendApplicationTokenModel
		frontendTokenToModel(&tokens[i], &refreshed)
		refreshed.ProjectID = state.ProjectID
		refreshed.ApplicationID = state.ApplicationID
		refreshed.AdoptTokenID = state.AdoptTokenID
		if state.RevokeOnDestroy.IsNull() || state.RevokeOnDestroy.IsUnknown() {
			refreshed.RevokeOnDestroy = types.BoolValue(true)
		} else {
			refreshed.RevokeOnDestroy = state.RevokeOnDestroy
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *FrontendApplicationTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state FrontendApplicationTokenModel
	var plan FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.RevokeOnDestroy = plan.RevokeOnDestroy
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FrontendApplicationTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	var state FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !shouldRevokeFrontendApplicationToken(state.RevokeOnDestroy) {
		return
	}
	err := r.client.RevokeFrontendApplicationToken(ctx, state.ProjectID.ValueString(), state.ApplicationID.ValueString(), state.ID.ValueString())
	if logclient.IsConflictError(err) {
		resp.Diagnostics.AddError(
			"Cannot revoke the last frontend application token",
			"Set revoke_on_destroy to false and apply that change before destroying the token resource with its application. Application deletion revokes all attached tokens. API response: "+err.Error(),
		)
	} else if err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Revoke frontend application token failed", err.Error())
	}
}

func shouldRevokeFrontendApplicationToken(value types.Bool) bool {
	return value.IsNull() || value.IsUnknown() || value.ValueBool()
}

func (r *FrontendApplicationTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportParts(strings.TrimSpace(req.ID), 3)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID format", `Expected "project_id/application_id/token_id". Example: terraform import logfire_frontend_application_token.token "project-id/application-id/token-id"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("application_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}
