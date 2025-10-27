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
)

var _ resource.Resource = &WriteTokenResource{}
var _ resource.ResourceWithConfigure = &WriteTokenResource{}

func NewWriteTokenResource() resource.Resource { return &WriteTokenResource{} }

type WriteTokenResource struct {
	client *APIClient
}

type WriteTokenModel struct {
	ID          types.String `tfsdk:"id"`
	ProjectID   types.String `tfsdk:"project_id"`
	Description types.String `tfsdk:"description"`
	Token       types.String `tfsdk:"token"`
	CreatedAt   types.String `tfsdk:"created_at"`
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
				Optional:            true,
				MarkdownDescription: "Optional description to help identify the token.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
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
		},
	}
}

func (r *WriteTokenResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*APIClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Expected *APIClient.")
		return
	}
	r.client = c
}

func writeTokenCreatePayload(m *WriteTokenModel) WriteTokenCreate {
	var desc *string
	if m != nil && !m.Description.IsNull() && !m.Description.IsUnknown() {
		v := m.Description.ValueString()
		desc = &v
	}
	return WriteTokenCreate{
		Description: desc,
	}
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

	projectID := plan.ProjectID.ValueString()
	payload := writeTokenCreatePayload(&plan)

	out, err := r.client.CreateWriteToken(ctx, projectID, payload)
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
		if out.Description != nil {
			state.Description = types.StringValue(*out.Description)
		} else if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
			state.Description = plan.Description
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
		state.Token = types.StringNull()
		if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
			state.Description = plan.Description
		} else {
			state.Description = types.StringNull()
		}
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

	projectID := state.ProjectID.ValueString()
	tokenID := state.ID.ValueString()

	items, err := r.client.ListWriteTokens(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError("List write tokens failed", err.Error())
		return
	}

	var found *WriteToken
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

	if found.Description != nil {
		newState.Description = types.StringValue(*found.Description)
	} else {
		newState.Description = state.Description
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

	projectID := state.ProjectID.ValueString()
	tokenID := state.ID.ValueString()

	if err := r.client.DeleteWriteToken(ctx, projectID, tokenID); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("Delete write token failed", err.Error())
	}
}
