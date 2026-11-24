// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
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

type FrontendApplicationTokenResource struct{ configured }

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
	useState := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Experimental: the backing management API is currently enabled only in Logfire staging environments. Manages one active token for a Logfire frontend application. Rotate in two applies with two resource blocks: add the replacement and deploy its `token`, then remove the old resource to revoke it. The provider never revokes an old token while creating a new one. Token issuance is not retried automatically because the operation is not idempotent; after an uncertain failure, list the application's tokens and adopt the one unambiguous new active token. Before destroying the whole application tree, set the last token's `revoke_on_destroy` to `false` in a separate apply; application deletion then revokes every attached token.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Frontend application token identifier.",
				PlanModifiers:       useState,
			},
			"project_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the project that owns the application.",
				PlanModifiers:       replace,
				Validators:          nonEmpty,
			},
			"application_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the frontend application.",
				PlanModifiers:       replace,
				Validators:          nonEmpty,
			},
			"adopt_token_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Existing active token to adopt instead of issuing another. Use the application's `token_id` for the initial handoff, then remove this resource only after a separately managed replacement has been deployed.",
				PlanModifiers:       replace,
				Validators:          nonEmpty,
			},
			"revoke_on_destroy": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Revoke this token when its resource is destroyed. Before destroying the entire application tree, set this to `false` in a separate apply for the last token so application deletion can revoke all attached tokens.",
			},
			"token": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "Active restricted browser token. The provider recovers this value on refresh and import.",
				PlanModifiers:       useState,
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Token status. A managed token remains in state only while active.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the token was created.",
				PlanModifiers:       useState,
			},
			"last_used_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the token was last used, when known.",
			},
		},
	}
}

func frontendTokenToModel(t *logclient.FrontendApplicationToken) FrontendApplicationTokenModel {
	return FrontendApplicationTokenModel{
		ID:         types.StringValue(t.ID),
		Token:      types.StringPointerValue(t.Token),
		Status:     types.StringValue(t.Status),
		CreatedAt:  types.StringValue(t.CreatedAt),
		LastUsedAt: types.StringPointerValue(t.LastUsedAt),
	}
}

// findActiveFrontendToken returns the token with the given ID, provided it is
// active and carries its secret.
func findActiveFrontendToken(tokens []logclient.FrontendApplicationToken, id string) *logclient.FrontendApplicationToken {
	for i := range tokens {
		t := &tokens[i]
		if t.ID == id && t.Status == "active" && t.Token != nil {
			return t
		}
	}
	return nil
}

func (r *FrontendApplicationTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID := plan.ProjectID.ValueString()
	applicationID := plan.ApplicationID.ValueString()
	var token *logclient.FrontendApplicationToken
	if plan.AdoptTokenID.IsNull() {
		out, err := r.client.CreateFrontendApplicationToken(ctx, projectID, applicationID)
		if err != nil {
			resp.Diagnostics.AddError("Create frontend application token failed", err.Error())
			return
		}
		token = &logclient.FrontendApplicationToken{
			ID:         out.TokenID,
			Status:     out.Status,
			Token:      &out.Token,
			CreatedAt:  out.CreatedAt,
			LastUsedAt: out.LastUsedAt,
		}
	} else {
		tokens, err := r.client.ListFrontendApplicationTokens(ctx, projectID, applicationID)
		if err != nil {
			resp.Diagnostics.AddError("Read adopted frontend application token failed", err.Error())
			return
		}
		token = findActiveFrontendToken(tokens, plan.AdoptTokenID.ValueString())
		if token == nil {
			resp.Diagnostics.AddError("Read frontend application token failed",
				"The API did not return the selected token as an active token for this application.")
			return
		}
	}
	state := frontendTokenToModel(token)
	state.ProjectID = plan.ProjectID
	state.ApplicationID = plan.ApplicationID
	state.AdoptTokenID = plan.AdoptTokenID
	state.RevokeOnDestroy = plan.RevokeOnDestroy
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FrontendApplicationTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tokens, err := r.client.ListFrontendApplicationTokens(ctx,
		state.ProjectID.ValueString(), state.ApplicationID.ValueString())
	if logclient.IsNotFoundError(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read frontend application token failed", err.Error())
		return
	}
	token := findActiveFrontendToken(tokens, state.ID.ValueString())
	if token == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	refreshed := frontendTokenToModel(token)
	refreshed.ProjectID = state.ProjectID
	refreshed.ApplicationID = state.ApplicationID
	refreshed.AdoptTokenID = state.AdoptTokenID
	refreshed.RevokeOnDestroy = types.BoolValue(
		shouldRevokeFrontendApplicationToken(state.RevokeOnDestroy))
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *FrontendApplicationTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.RevokeOnDestroy = plan.RevokeOnDestroy
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FrontendApplicationTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FrontendApplicationTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !shouldRevokeFrontendApplicationToken(state.RevokeOnDestroy) {
		return
	}
	err := r.client.RevokeFrontendApplicationToken(ctx, state.ProjectID.ValueString(),
		state.ApplicationID.ValueString(), state.ID.ValueString())
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
		resp.Diagnostics.AddError("Invalid import ID format",
			`Expected "project_id/application_id/token_id". Example: terraform import logfire_frontend_application_token.token "project-id/application-id/token-id"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("application_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}
