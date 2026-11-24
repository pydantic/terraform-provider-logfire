// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

type FrontendApplicationResource struct{ configured }

type FrontendApplicationModel struct {
	ID                       types.String `tfsdk:"id"`
	ProjectID                types.String `tfsdk:"project_id"`
	Name                     types.String `tfsdk:"name"`
	ServiceNamespace         types.String `tfsdk:"service_namespace"`
	Environment              types.String `tfsdk:"environment"`
	AdoptExistingServiceName types.Bool   `tfsdk:"adopt_existing_service_name"`
	CreatedAt                types.String `tfsdk:"created_at"`
	TokenID                  types.String `tfsdk:"token_id"`
	Token                    types.String `tfsdk:"token"`
}

func (r *FrontendApplicationResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_frontend_application"
}

func (r *FrontendApplicationResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	useState := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	const renameDescription = "Requires replacement unless the new value is the application's current name in Logfire."
	// Read keeps the configured name after a UI rename, so the config can catch up.
	replaceUnlessServerName := stringplanmodifier.RequiresReplaceIf(func(ctx context.Context,
		req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse,
	) {
		serverName, diags := req.Private.GetKey(ctx, serverNameKey)
		resp.Diagnostics.Append(diags...)
		planned := serverNameValue(req.PlanValue.ValueString())
		resp.RequiresReplace = !bytes.Equal(serverName, planned)
	}, renameDescription, renameDescription)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Experimental: the backing management API is currently enabled only in Logfire staging environments. Manages an immutable Logfire frontend application identity and exposes one active restricted browser token. Use `logfire_frontend_application_token` for two-phase rotation. Creation is not retried automatically because the operation is not idempotent; after an uncertain failure, list the applications and import the matching identity instead of applying again.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Frontend application identifier.",
				PlanModifiers:       useState,
			},
			"project_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the project that owns the application.",
				PlanModifiers:       replace,
				Validators:          nonEmpty,
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Application name. Logfire overwrites `service.name` with this value. The API cannot rename an application, so changing this replaces it, unless the new value is its current name in Logfire. After a rename in the Logfire UI, Terraform keeps the configured name and plans no change. Update this value to the new name to follow the rename without a replacement.",
				PlanModifiers:       []planmodifier.String{replaceUnlessServerName},
				Validators:          nonEmpty,
			},
			"service_namespace": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Immutable value used to overwrite `service.namespace` when set.",
				PlanModifiers:       replace,
			},
			"environment": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Immutable value used to overwrite `deployment.environment.name` when set.",
				PlanModifiers:       replace,
			},
			"adopt_existing_service_name": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Allow creation when telemetry already reports under `name`. Set this before replacing `service_namespace` or `environment` without changing `name`.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the application was created.",
				PlanModifiers:       useState,
			},
			"token_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of an active restricted browser token.",
				PlanModifiers:       useState,
			},
			"token": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "An active restricted browser token. The provider recovers this value on refresh and import.",
				PlanModifiers:       useState,
			},
		},
	}
}

func (r *FrontendApplicationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.AdoptExistingServiceName.IsUnknown() {
		return
	}
	serverName, diags := req.Private.GetKey(ctx, serverNameKey)
	resp.Diagnostics.Append(diags...)
	if sameNameIdentityReplacementNeedsAdoption(state, plan, serverName) {
		resp.Diagnostics.AddAttributeError(
			path.Root("adopt_existing_service_name"),
			"Same-name replacement requires adoption",
			"Set adopt_existing_service_name to true before changing "+
				"service_namespace or environment without changing name. Use "+
				"lifecycle.create_before_destroy when environment changes. A "+
				"namespace-only replacement keeps the same unique identity, so "+
				"it must use destroy-before-create and interrupts ingestion; use "+
				"a separately named application for a zero-downtime handoff.",
		)
	}
}

// serverName is the name Logfire reported on the last read, which differs from state after a UI
// rename. A plan that matches either keeps the name, so it still needs adoption. Logfire checks
// names within one project, so a move to another project never needs it.
func sameNameIdentityReplacementNeedsAdoption(state, plan FrontendApplicationModel, serverName []byte) bool {
	if plan.Name.IsUnknown() || !state.ProjectID.Equal(plan.ProjectID) {
		return false
	}
	planned := plan.Name.ValueString()
	if state.Name.ValueString() != planned &&
		!bytes.Equal(serverName, serverNameValue(planned)) {
		return false
	}
	identityChanged := !state.ServiceNamespace.Equal(plan.ServiceNamespace) ||
		!state.Environment.Equal(plan.Environment)
	return identityChanged && !plan.AdoptExistingServiceName.ValueBool()
}

const serverNameKey = "server_name"

// Private state needs valid JSON. Marshaling a string cannot fail.
func serverNameValue(name string) []byte {
	b, _ := json.Marshal(name)
	return b
}

func frontendApplicationToModel(a *logclient.FrontendApplication) FrontendApplicationModel {
	return FrontendApplicationModel{
		ID:               types.StringValue(a.ID),
		ProjectID:        types.StringValue(a.ProjectID),
		Name:             types.StringValue(a.Name),
		ServiceNamespace: types.StringPointerValue(a.ServiceNamespace),
		Environment:      types.StringPointerValue(a.Environment),
		CreatedAt:        types.StringValue(a.CreatedAt),
	}
}

func (r *FrontendApplicationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FrontendApplicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateFrontendApplication(ctx, plan.ProjectID.ValueString(),
		logclient.FrontendApplicationCreate{
			Name:                     plan.Name.ValueString(),
			ServiceNamespace:         plan.ServiceNamespace.ValueStringPointer(),
			Environment:              plan.Environment.ValueStringPointer(),
			AdoptExistingServiceName: plan.AdoptExistingServiceName.ValueBool(),
		})
	if err != nil {
		resp.Diagnostics.AddError("Create frontend application failed", err.Error())
		return
	}
	state := frontendApplicationToModel(&out.FrontendApplication)
	state.AdoptExistingServiceName = plan.AdoptExistingServiceName
	state.TokenID = types.StringValue(out.TokenID)
	state.Token = types.StringValue(out.Token)
	tflog.Trace(ctx, "created frontend application",
		map[string]any{"id": out.ID, "project_id": out.ProjectID})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func selectActiveFrontendToken(tokens []logclient.FrontendApplicationToken, preferredID string) *logclient.FrontendApplicationToken {
	var first *logclient.FrontendApplicationToken
	for i := range tokens {
		t := &tokens[i]
		if t.Status != "active" || t.Token == nil {
			continue
		}
		if t.ID == preferredID {
			return t
		}
		if first == nil {
			first = t
		}
	}
	return first
}

func (r *FrontendApplicationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	application, status, err := r.client.GetFrontendApplication(ctx,
		state.ProjectID.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read frontend application failed", err.Error())
		return
	}
	if status == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	refreshed := frontendApplicationToModel(application)
	refreshed.AdoptExistingServiceName = state.AdoptExistingServiceName
	resp.Diagnostics.Append(resp.Private.SetKey(
		ctx, serverNameKey, serverNameValue(application.Name))...)
	if !state.Name.IsNull() {
		refreshed.Name = state.Name
	}
	tokens, err := r.client.ListFrontendApplicationTokens(ctx,
		application.ProjectID, application.ID)
	if err != nil {
		resp.Diagnostics.AddError("Read frontend application tokens failed", err.Error())
		return
	}
	active := selectActiveFrontendToken(tokens, state.TokenID.ValueString())
	if active == nil {
		resp.Diagnostics.AddWarning(
			"Frontend application has no active token",
			"The application remains managed, but its token outputs are "+
				"null. Add a logfire_frontend_application_token resource to "+
				"issue a replacement.",
		)
	} else {
		refreshed.TokenID = types.StringValue(active.ID)
		refreshed.Token = types.StringValue(*active.Token)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *FrontendApplicationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Name = plan.Name
	state.AdoptExistingServiceName = plan.AdoptExistingServiceName
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FrontendApplicationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteFrontendApplication(ctx,
		state.ProjectID.ValueString(), state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Delete frontend application failed", err.Error())
	}
}

func (r *FrontendApplicationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportParts(strings.TrimSpace(req.ID), 2)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID format",
			`Expected "project_id/application_id". Example: `+
				`terraform import logfire_frontend_application.app `+
				`"project-id/application-id"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
