// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

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

var _ resource.Resource = &WriteTokenResource{}
var _ resource.ResourceWithConfigure = &WriteTokenResource{}

func NewWriteTokenResource() resource.Resource { return &WriteTokenResource{} }

type WriteTokenResource struct {
	client *logclient.APIClient
}

type WriteTokenModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Description   types.String `tfsdk:"description"`
	Token         types.String `tfsdk:"token"`
	CreatedAt     types.String `tfsdk:"created_at"`
	ProjectName   types.String `tfsdk:"project_name"`
	CreatedByName types.String `tfsdk:"created_by_name"`
	TokenPrefix   types.String `tfsdk:"token_prefix"`
}

func (r *WriteTokenResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_write_token"
}

func (r *WriteTokenResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire write token.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Write token identifier.",
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
				MarkdownDescription: "Description is fixed to \"Created by Public API\" for provider-managed tokens.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token": rschema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The generated write token. Only returned on creation.",
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
				MarkdownDescription: "Prefix of the generated write token.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *WriteTokenResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *WriteTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var plan WriteTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ProjectID.IsNull() || plan.ProjectID.IsUnknown() || plan.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "The write token requires a project_id to construct API paths.")
		return
	}

	projectID := plan.ProjectID.ValueString()

	out, err := r.client.CreateWriteToken(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Create write token failed", err.Error())
		return
	}

	var state WriteTokenModel
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
		state.TokenPrefix = types.StringValue(out.TokenPrefix)
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

	tflog.Trace(ctx, "created write token", map[string]any{"id": state.ID.ValueString(), "project_id": state.ProjectID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *WriteTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state WriteTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot read write token because the state is missing a project_id.")
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot read write token because the state is missing an ID.")
		return
	}

	projectID := state.ProjectID.ValueString()
	tokenID := state.ID.ValueString()

	items, err := r.client.ListWriteTokens(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("List write tokens failed", err.Error())
		return
	}

	var found *logclient.WriteToken
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

	var newState WriteTokenModel
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
	newState.TokenPrefix = types.StringValue(found.TokenPrefix)
	if found.TokenPrefix == "" && !state.TokenPrefix.IsNull() && !state.TokenPrefix.IsUnknown() {
		newState.TokenPrefix = state.TokenPrefix
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

func (r *WriteTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "Write tokens cannot be updated; Terraform should plan a replacement.")
}

func (r *WriteTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}

	var state WriteTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ProjectID.IsNull() || state.ProjectID.IsUnknown() || state.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing project_id", "Cannot delete write token because the state is missing a project_id.")
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() || state.ID.ValueString() == "" {
		resp.Diagnostics.AddError("Missing ID", "Cannot delete write token because the state is missing an ID.")
		return
	}

	projectID := state.ProjectID.ValueString()
	tokenID := state.ID.ValueString()

	if err := r.client.DeleteWriteToken(ctx, projectID, tokenID); err != nil {
		if logclient.IsNotFoundError(err) {
			// Already gone, treat as successful delete
			return
		}
		resp.Diagnostics.AddError("Delete write token failed", err.Error())
	}
}
