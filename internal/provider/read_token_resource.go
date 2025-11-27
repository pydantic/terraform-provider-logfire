// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"net/http"

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

var _ resource.Resource = &ReadTokenResource{}
var _ resource.ResourceWithConfigure = &ReadTokenResource{}

func NewReadTokenResource() resource.Resource { return &ReadTokenResource{} }

type ReadTokenResource struct {
	client *logclient.APIClient
}

type ReadTokenModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Description   types.String `tfsdk:"description"`
	Token         types.String `tfsdk:"token"`
	CreatedAt     types.String `tfsdk:"created_at"`
	ProjectName   types.String `tfsdk:"project_name"`
	CreatedByName types.String `tfsdk:"created_by_name"`
	TokenPrefix   types.String `tfsdk:"token_prefix"`
}

func (r *ReadTokenResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_read_token"
}

func (r *ReadTokenResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire read token.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Read token identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the project that owns the token.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"description": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Description assigned by the Logfire API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token": rschema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The generated read token. Only returned on creation.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the token was created.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the project that owns the token.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_by_name": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Display name of the user that created the token.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token_prefix": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Prefix of the generated read token.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *ReadTokenResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*logclient.APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Expected *APIClient.")
		return
	}
	r.client = c
}

func (r *ReadTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan ReadTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ProjectID.IsNull() || plan.ProjectID.IsUnknown() || plan.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "The read token requires a project_id to construct API paths.")
		return
	}

	projectID := plan.ProjectID.ValueString()

	out, err := r.client.CreateReadToken(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Create read token failed", err.Error())
		return
	}

	var state ReadTokenModel
	if out != nil {
		if out.ID != "" {
			state.ID = types.StringValue(out.ID)
		} else {
			state.ID = types.StringNull()
		}

		if out.ProjectID != "" {
			state.ProjectID = types.StringValue(out.ProjectID)
		} else {
			state.ProjectID = types.StringValue(projectID)
		}

		if out.CreatedAt != "" {
			state.CreatedAt = types.StringValue(out.CreatedAt)
		} else {
			state.CreatedAt = types.StringNull()
		}

		if out.ProjectName != "" {
			state.ProjectName = types.StringValue(out.ProjectName)
		} else {
			state.ProjectName = types.StringNull()
		}

		if out.CreatedByName != nil {
			state.CreatedByName = types.StringValue(*out.CreatedByName)
		} else {
			state.CreatedByName = types.StringNull()
		}

		if out.TokenPrefix != "" {
			state.TokenPrefix = types.StringValue(out.TokenPrefix)
		} else {
			state.TokenPrefix = types.StringNull()
		}

		if out.Description != nil {
			state.Description = types.StringValue(*out.Description)
		} else {
			state.Description = types.StringNull()
		}

		if out.Token != nil {
			state.Token = types.StringValue(*out.Token)
		} else {
			state.Token = types.StringNull()
		}
	} else {
		state.ID = types.StringNull()
		state.ProjectID = types.StringValue(projectID)
		state.CreatedAt = types.StringNull()
		state.ProjectName = types.StringNull()
		state.CreatedByName = types.StringNull()
		state.TokenPrefix = types.StringNull()
		state.Token = types.StringNull()
		state.Description = types.StringNull()
	}

	tflog.Trace(ctx, "created read token", map[string]any{"id": state.ID.ValueString(), "project_id": state.ProjectID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ReadTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ReadTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot read read token because the state is missing a project_id.")
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot read read token because the state is missing an ID.")
		return
	}

	projectID := state.ProjectID.ValueString()
	tokenID := state.ID.ValueString()

	items, err := r.client.ListReadTokens(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("List read tokens failed", err.Error())
		return
	}

	var found *logclient.ReadToken
	for i := range items {
		if items[i].ID == tokenID {
			found = &items[i]
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	var newState ReadTokenModel
	newState.ID = types.StringValue(found.ID)

	if found.ProjectID != "" {
		newState.ProjectID = types.StringValue(found.ProjectID)
	} else {
		newState.ProjectID = state.ProjectID
	}

	if found.CreatedAt != "" {
		newState.CreatedAt = types.StringValue(found.CreatedAt)
	} else {
		newState.CreatedAt = state.CreatedAt
	}

	if found.ProjectName != "" {
		newState.ProjectName = types.StringValue(found.ProjectName)
	} else {
		newState.ProjectName = state.ProjectName
	}

	if found.CreatedByName != nil {
		newState.CreatedByName = types.StringValue(*found.CreatedByName)
	} else {
		newState.CreatedByName = state.CreatedByName
	}

	if found.TokenPrefix != "" {
		newState.TokenPrefix = types.StringValue(found.TokenPrefix)
	} else if !state.TokenPrefix.IsNull() && !state.TokenPrefix.IsUnknown() {
		newState.TokenPrefix = state.TokenPrefix
	} else {
		newState.TokenPrefix = types.StringNull()
	}

	if found.Description != nil {
		newState.Description = types.StringValue(*found.Description)
	} else {
		newState.Description = types.StringNull()
	}

	if found.Token != nil {
		newState.Token = types.StringValue(*found.Token)
	} else if !state.Token.IsNull() && !state.Token.IsUnknown() {
		newState.Token = state.Token
	} else {
		newState.Token = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *ReadTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "Read tokens cannot be updated; Terraform should plan a replacement.")
}

func (r *ReadTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state ReadTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot delete read token because the state is missing a project_id.")
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot delete read token because the state is missing an ID.")
		return
	}

	projectID := state.ProjectID.ValueString()
	tokenID := state.ID.ValueString()

	if err := r.client.DeleteReadToken(ctx, projectID, tokenID); err != nil {
		var apiErr *logclient.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("Delete read token failed", err.Error())
	}
}
