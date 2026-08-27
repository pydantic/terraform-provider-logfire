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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var _ resource.Resource = &FrontendApplicationResource{}
var _ resource.ResourceWithConfigure = &FrontendApplicationResource{}
var _ resource.ResourceWithImportState = &FrontendApplicationResource{}
var _ resource.ResourceWithModifyPlan = &FrontendApplicationResource{}

func NewFrontendApplicationResource() resource.Resource { return &FrontendApplicationResource{} }

type FrontendApplicationResource struct {
	client *logclient.APIClient
}

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
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an immutable Logfire frontend application identity and exposes one active restricted browser token. Use `logfire_frontend_application_token` for two-phase rotation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Frontend application identifier.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "UUID of the project that owns the application.", PlanModifiers: replace,
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Immutable application name. Logfire overwrites `service.name` with this value.", PlanModifiers: replace,
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"service_namespace": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Immutable value used to overwrite `service.namespace` when set.", PlanModifiers: replace,
			},
			"environment": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Immutable value used to overwrite `deployment.environment.name` when set.", PlanModifiers: replace,
			},
			"adopt_existing_service_name": schema.BoolAttribute{
				Optional: true, MarkdownDescription: "Allow creation when telemetry already reports under `name`. Set this before replacing `service_namespace` or `environment` without changing `name`.",
			},
			"created_at": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Timestamp when the application was created.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"token_id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Identifier of an active restricted browser token.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"token": schema.StringAttribute{
				Computed: true, Sensitive: true, MarkdownDescription: "An active restricted browser token. The provider recovers this value on refresh and import.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *FrontendApplicationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state FrontendApplicationModel
	var plan FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.AdoptExistingServiceName.IsUnknown() {
		return
	}
	if sameNameIdentityReplacementNeedsAdoption(state, plan) {
		resp.Diagnostics.AddAttributeError(
			path.Root("adopt_existing_service_name"),
			"Same-name replacement requires adoption",
			"Set adopt_existing_service_name to true before changing service_namespace or environment without changing name. Use lifecycle.create_before_destroy when environment changes. A namespace-only replacement keeps the same unique identity, so it must use destroy-before-create and interrupts ingestion; use a separately named application for a zero-downtime handoff.",
		)
	}
}

func sameNameIdentityReplacementNeedsAdoption(state, plan FrontendApplicationModel) bool {
	if state.Name.IsNull() || state.Name.IsUnknown() || plan.Name.IsNull() || plan.Name.IsUnknown() || state.Name.ValueString() != plan.Name.ValueString() {
		return false
	}
	identityChanged := !state.ServiceNamespace.Equal(plan.ServiceNamespace) || !state.Environment.Equal(plan.Environment)
	adopts := !plan.AdoptExistingServiceName.IsNull() && !plan.AdoptExistingServiceName.IsUnknown() && plan.AdoptExistingServiceName.ValueBool()
	return identityChanged && !adopts
}

func (r *FrontendApplicationResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func optionalString(value types.String) *string {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	v := value.ValueString()
	return &v
}

func setOptionalString(target *types.String, value *string) {
	if value == nil {
		*target = types.StringNull()
	} else {
		*target = types.StringValue(*value)
	}
}

func frontendApplicationToModel(application *logclient.FrontendApplication, state *FrontendApplicationModel) {
	state.ID = types.StringValue(application.ID)
	state.ProjectID = types.StringValue(application.ProjectID)
	state.Name = types.StringValue(application.Name)
	setOptionalString(&state.ServiceNamespace, application.ServiceNamespace)
	setOptionalString(&state.Environment, application.Environment)
	state.CreatedAt = types.StringValue(application.CreatedAt)
}

func (r *FrontendApplicationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	var plan FrontendApplicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateFrontendApplication(ctx, plan.ProjectID.ValueString(), logclient.FrontendApplicationCreate{
		Name: plan.Name.ValueString(), ServiceNamespace: optionalString(plan.ServiceNamespace), Environment: optionalString(plan.Environment),
		AdoptExistingServiceName: !plan.AdoptExistingServiceName.IsNull() && plan.AdoptExistingServiceName.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Create frontend application failed", err.Error())
		return
	}
	var state FrontendApplicationModel
	frontendApplicationToModel(&out.FrontendApplication, &state)
	state.AdoptExistingServiceName = plan.AdoptExistingServiceName
	state.TokenID = types.StringValue(out.TokenID)
	state.Token = types.StringValue(out.Token)
	tflog.Trace(ctx, "created frontend application", map[string]any{"id": out.ID, "project_id": out.ProjectID})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func selectActiveFrontendToken(tokens []logclient.FrontendApplicationToken, preferredID string) *logclient.FrontendApplicationToken {
	for i := range tokens {
		if tokens[i].ID == preferredID && tokens[i].Status == "active" && tokens[i].Token != nil {
			return &tokens[i]
		}
	}
	for i := range tokens {
		if tokens[i].Status == "active" && tokens[i].Token != nil {
			return &tokens[i]
		}
	}
	return nil
}

func (r *FrontendApplicationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	var state FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	application, status, err := r.client.GetFrontendApplication(ctx, state.ProjectID.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read frontend application failed", err.Error())
		return
	}
	if status == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	var refreshed FrontendApplicationModel
	frontendApplicationToModel(application, &refreshed)
	refreshed.AdoptExistingServiceName = state.AdoptExistingServiceName
	tokens, err := r.client.ListFrontendApplicationTokens(ctx, application.ProjectID, application.ID)
	if err != nil {
		resp.Diagnostics.AddError("Read frontend application tokens failed", err.Error())
		return
	}
	preferredID := ""
	if !state.TokenID.IsNull() && !state.TokenID.IsUnknown() {
		preferredID = state.TokenID.ValueString()
	}
	active := selectActiveFrontendToken(tokens, preferredID)
	if active == nil {
		resp.Diagnostics.AddError("Frontend application has no active token", "Create a replacement token before refreshing this resource.")
		return
	}
	refreshed.TokenID = types.StringValue(active.ID)
	refreshed.Token = types.StringValue(*active.Token)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *FrontendApplicationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state FrontendApplicationModel
	var plan FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.AdoptExistingServiceName = plan.AdoptExistingServiceName
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FrontendApplicationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Not configured", "The provider is not configured.")
		return
	}
	var state FrontendApplicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteFrontendApplication(ctx, state.ProjectID.ValueString(), state.ID.ValueString()); err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Delete frontend application failed", err.Error())
	}
}

func (r *FrontendApplicationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportParts(strings.TrimSpace(req.ID), 2)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID format", `Expected "project_id/application_id". Example: terraform import logfire_frontend_application.app "project-id/application-id"`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
