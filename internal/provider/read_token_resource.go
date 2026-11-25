// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// tokenResource implements logfire_read_token and logfire_write_token, which
// differ only in the client calls behind them. The client returns the same
// shape for both kinds, so the write calls convert to the read types.
type tokenResource struct {
	configured
	kind   string
	create func(ctx context.Context, c *logclient.APIClient, projectID string,
		expiresAt *string) (*logclient.ReadToken, error)
	// find returns nil when the project has no token with that ID.
	find func(ctx context.Context, c *logclient.APIClient, projectID,
		tokenID string) (*logclient.ReadToken, error)
	remove func(ctx context.Context, c *logclient.APIClient, projectID,
		tokenID string) error
}

type tokenModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Description   types.String `tfsdk:"description"`
	Token         types.String `tfsdk:"token"`
	CreatedAt     types.String `tfsdk:"created_at"`
	ExpiresAt     types.String `tfsdk:"expires_at"`
	ProjectName   types.String `tfsdk:"project_name"`
	CreatedByName types.String `tfsdk:"created_by_name"`
	TokenPrefix   types.String `tfsdk:"token_prefix"`
}

func NewReadTokenResource() resource.Resource {
	return &tokenResource{
		kind: "read",
		create: func(ctx context.Context, c *logclient.APIClient,
			projectID string, expiresAt *string) (*logclient.ReadToken, error) {
			return c.CreateReadToken(ctx, projectID,
				logclient.CreateReadTokenInput{ExpiresAt: expiresAt})
		},
		find: func(ctx context.Context, c *logclient.APIClient,
			projectID, tokenID string) (*logclient.ReadToken, error) {
			items, err := c.ListReadTokens(ctx, projectID)
			return findToken(items, tokenID), err
		},
		remove: func(ctx context.Context, c *logclient.APIClient,
			projectID, tokenID string) error {
			return c.DeleteReadToken(ctx, projectID, tokenID)
		},
	}
}

// findToken returns the token with the given ID as a read token.
func findToken[T logclient.ReadToken | logclient.WriteToken](items []T, id string) *logclient.ReadToken {
	for _, item := range items {
		if t := logclient.ReadToken(item); t.ID == id {
			return &t
		}
	}
	return nil
}

// tokenToModel maps a token into state. Values the API leaves out keep what
// prior holds: Terraform learns the secret only when the token is created.
func tokenToModel(t *logclient.ReadToken, prior tokenModel) tokenModel {
	model := tokenModel{
		ID:            types.StringValue(t.ID),
		ProjectID:     stringOr(t.ProjectID, prior.ProjectID),
		Description:   types.StringPointerValue(t.Description),
		Token:         prior.Token,
		CreatedAt:     stringOr(t.CreatedAt, prior.CreatedAt),
		ExpiresAt:     types.StringPointerValue(t.ExpiresAt),
		ProjectName:   stringOr(t.ProjectName, prior.ProjectName),
		CreatedByName: prior.CreatedByName,
		TokenPrefix:   stringOr(t.TokenPrefix, prior.TokenPrefix),
	}
	if t.Token != nil {
		model.Token = types.StringValue(*t.Token)
	}
	if t.CreatedByName != nil {
		model.CreatedByName = types.StringValue(*t.CreatedByName)
	}
	return model
}

func stringOr(s string, fallback types.String) types.String {
	if s == "" {
		return fallback
	}
	return types.StringValue(s)
}

func (r *tokenResource) title() string {
	return strings.ToUpper(r.kind[:1]) + r.kind[1:]
}

func (r *tokenResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind + "_token"
}

func (r *tokenResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	useState := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = rschema.Schema{
		MarkdownDescription: fmt.Sprintf("Manages a Logfire %[1]s token. Equivalent to logfire_api_key with scopes `[\"project:%[1]s_otlp\"]`. Creating it requires the `project:%[1]s_token` scope on a credential that can reach the project. Import is not supported: the API returns the token value only at creation, so an imported token could never reproduce its secret in state. Recreate a token with Terraform to adopt it.", r.kind),
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: r.title() + " token identifier.",
				PlanModifiers:       useState,
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the project that owns the token.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"description": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Description is fixed to \"Created by Public API\" for provider-managed tokens.",
				PlanModifiers:       useState,
			},
			"token": rschema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "The generated " + r.kind +
					" token. Only returned on creation.",
				PlanModifiers: useState,
			},
			"created_at": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the token was created.",
				PlanModifiers:       useState,
			},
			"expires_at": rschema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional RFC3339 expiration timestamp for the token (for example `2026-12-31T23:59:59Z`). If omitted, the token does not expire.",
				Validators:          []validator.String{newOptionalRFC3339Validator()},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"project_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the project that owns the token.",
				PlanModifiers:       useState,
			},
			"created_by_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Display name of the user that created the token.",
				PlanModifiers:       useState,
			},
			"token_prefix": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Prefix of the generated " + r.kind + " token.",
				PlanModifiers:       useState,
			},
		},
	}
}

func (r *tokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if invalidExpiresAt(plan.ExpiresAt, &resp.Diagnostics) {
		return
	}

	out, err := r.create(ctx, r.client, plan.ProjectID.ValueString(),
		expiresAtPointer(plan.ExpiresAt))
	if err != nil {
		resp.Diagnostics.AddError("Create "+r.kind+" token failed", err.Error())
		return
	}

	state := tokenToModel(out, tokenModel{ProjectID: plan.ProjectID})
	tflog.Trace(ctx, "created "+r.kind+" token", map[string]any{
		"id": state.ID.ValueString(), "project_id": state.ProjectID.ValueString(),
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	projectID := state.ProjectID.ValueString()
	found, err := r.find(ctx, r.client, projectID, state.ID.ValueString())
	if err != nil {
		if projectMissing(ctx, r.client, projectID, err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("List "+r.kind+" tokens failed", err.Error())
		return
	}
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	newState := tokenToModel(found, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *tokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", r.title()+
		" tokens cannot be updated; Terraform should plan a replacement.")
}

func (r *tokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ProjectID.ValueString()
	err := r.remove(ctx, r.client, projectID, state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) &&
		!projectMissing(ctx, r.client, projectID, err) {
		resp.Diagnostics.AddError("Delete "+r.kind+" token failed", err.Error())
	}
}
