// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// gatewayGuardrailBetaNote follows the first sentence of every guardrail resource description.
const gatewayGuardrailBetaNote = "~> **Beta:** this resource is backed by a Logfire API in Beta. Its schema and behavior may change in backwards-incompatible ways before general availability."

// gatewayGuardrailScopesNote states the plan and scopes that every guardrail resource needs.
const gatewayGuardrailScopesNote = "Requires Growth, Enterprise Cloud, or self-hosted and a token with organization:read_gateway and organization:write_gateway scopes (organization:read and organization:write are also accepted)."

var gatewayEndpointSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// GatewayGuardrailTargetModel applies a guardrail action to all endpoints or to one endpoint.
type GatewayGuardrailTargetModel struct {
	EndpointSlug types.String `tfsdk:"endpoint_slug"`
	Action       types.String `tfsdk:"action"`
}

// GatewayGuardrailSampleMessageModel is one editor sample of a prebuilt or regex guardrail.
type GatewayGuardrailSampleMessageModel struct {
	Role    types.String `tfsdk:"role"`
	Content types.String `tfsdk:"content"`
}

var gatewayGuardrailSampleMessageType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"role":    types.StringType,
	"content": types.StringType,
}}

// gatewayGuardrailTargetsAttribute is the shared `targets` schema of the guardrail resources.
func gatewayGuardrailTargetsAttribute() schema.SetNestedAttribute {
	return schema.SetNestedAttribute{
		Required:            true,
		MarkdownDescription: "Complete set of targets. A change replaces the whole set in one request. A target without `endpoint_slug` has the type `all`. It applies to every endpoint and cannot be combined with other targets. If an endpoint is deleted, Logfire deletes its targets, and the next plan shows them as a change.",
		Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"endpoint_slug": schema.StringAttribute{
					Optional:            true,
					MarkdownDescription: "Slug of the endpoint the target applies to, for example `logfire_gateway_endpoint.default.slug`. Omit it to apply the target to every endpoint.",
					Validators: []validator.String{
						stringvalidator.LengthBetween(1, 64),
						stringvalidator.RegexMatches(gatewayEndpointSlugPattern,
							"must start with a letter or digit and contain only letters, digits, dots, underscores, or hyphens"),
					},
				},
				"action": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "What the Gateway does on a match: `observe`, `flag`, `redact`, or `block`. Every action other than `observe` needs a plan with guardrail enforcement.",
					Validators:          []validator.String{stringvalidator.OneOf("observe", "flag", "redact", "block")},
				},
			},
		},
	}
}

// gatewayGuardrailSampleMessagesAttribute is the shared `sample_messages` schema
// of the prebuilt and regex guardrail resources. The two APIs have different limits.
func gatewayGuardrailSampleMessagesAttribute(maxMessages, maxContent int) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: "Sample messages that the Logfire UI shows in the guardrail editor. They do not change what the Gateway enforces. If you omit this attribute, Terraform keeps the samples stored in Logfire, including samples set in the UI. Set it to `[]` to remove them. " +
			fmt.Sprintf("At most %d messages.", maxMessages),
		Validators:    []validator.List{listvalidator.SizeAtMost(maxMessages)},
		PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"role": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Message role: `system`, `user`, `assistant`, or `tool`.",
					Validators:          []validator.String{stringvalidator.OneOf("system", "user", "assistant", "tool")},
				},
				"content": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: fmt.Sprintf("Message text. At most %d characters.", maxContent),
					Validators:          []validator.String{stringvalidator.LengthAtMost(maxContent)},
				},
			},
		},
	}
}

func gatewayGuardrailTargetsToAPI(targets []GatewayGuardrailTargetModel) []logclient.GatewayGuardrailTarget {
	out := make([]logclient.GatewayGuardrailTarget, 0, len(targets))
	for _, target := range targets {
		scope := logclient.GatewayGuardrailTargetScope{Type: "all"}
		if !target.EndpointSlug.IsNull() {
			scope = logclient.GatewayGuardrailTargetScope{Type: "endpoint", EndpointSlug: target.EndpointSlug.ValueString()}
		}
		out = append(out, logclient.GatewayGuardrailTarget{Target: scope, Action: target.Action.ValueString()})
	}
	return out
}

func gatewayGuardrailTargetsFromAPI(targets []logclient.GatewayGuardrailTarget) []GatewayGuardrailTargetModel {
	out := make([]GatewayGuardrailTargetModel, 0, len(targets))
	for _, target := range targets {
		slug := types.StringNull()
		if target.Target.Type == "endpoint" {
			slug = types.StringValue(target.Target.EndpointSlug)
		}
		out = append(out, GatewayGuardrailTargetModel{EndpointSlug: slug, Action: types.StringValue(target.Action)})
	}
	return out
}

// validateGatewayGuardrailTargets repeats the target rules that the API enforces on the whole set.
func validateGatewayGuardrailTargets(ctx context.Context, config tfsdk.Config, diags *diag.Diagnostics) {
	var set types.Set
	diags.Append(config.GetAttribute(ctx, path.Root("targets"), &set)...)
	if diags.HasError() || set.IsNull() || set.IsUnknown() {
		return
	}
	for _, element := range set.Elements() {
		if element.IsUnknown() {
			return
		}
	}
	var targets []GatewayGuardrailTargetModel
	diags.Append(set.ElementsAs(ctx, &targets, false)...)
	if diags.HasError() {
		return
	}
	seen := map[string]bool{}
	allEndpoints, oneEndpoint := false, false
	for _, target := range targets {
		if target.EndpointSlug.IsUnknown() {
			continue
		}
		key := "*"
		if target.EndpointSlug.IsNull() {
			allEndpoints = true
		} else {
			oneEndpoint = true
			key = target.EndpointSlug.ValueString()
		}
		if seen[key] {
			detail := "Each target must appear once. More than one target omits endpoint_slug."
			if key != "*" {
				detail = "Each target must appear once. Endpoint " + key + " appears in more than one target."
			}
			diags.AddAttributeError(path.Root("targets"), "Duplicate target", detail)
		}
		seen[key] = true
	}
	if allEndpoints && oneEndpoint {
		diags.AddAttributeError(path.Root("targets"), "Mixed targets",
			"A target without endpoint_slug has the type all, which cannot be combined with other targets.")
	}
}

// gatewayGuardrailSampleMessagesToAPI returns the configured samples, or nil
// when the configuration omits them. Logfire keeps the stored samples for nil.
func gatewayGuardrailSampleMessagesToAPI(ctx context.Context, config tfsdk.Config, planned types.List, diags *diag.Diagnostics) *[]logclient.GatewayGuardrailSampleMessage {
	var configured types.List
	diags.Append(config.GetAttribute(ctx, path.Root("sample_messages"), &configured)...)
	if diags.HasError() || configured.IsNull() || planned.IsNull() || planned.IsUnknown() {
		return nil
	}
	var samples []GatewayGuardrailSampleMessageModel
	diags.Append(planned.ElementsAs(ctx, &samples, false)...)
	out := make([]logclient.GatewayGuardrailSampleMessage, 0, len(samples))
	for _, sample := range samples {
		out = append(out, logclient.GatewayGuardrailSampleMessage{
			Role: sample.Role.ValueString(), Content: sample.Content.ValueString(),
		})
	}
	return &out
}

func gatewayGuardrailSampleMessagesFromAPI(ctx context.Context, samples []logclient.GatewayGuardrailSampleMessage, diags *diag.Diagnostics) types.List {
	models := make([]GatewayGuardrailSampleMessageModel, 0, len(samples))
	for _, sample := range samples {
		models = append(models, GatewayGuardrailSampleMessageModel{
			Role: types.StringValue(sample.Role), Content: types.StringValue(sample.Content),
		})
	}
	list, d := types.ListValueFrom(ctx, gatewayGuardrailSampleMessageType, models)
	diags.Append(d...)
	return list
}

// gatewayGuardrailSampleTextsToAPI returns the configured Presidio samples, or
// nil when the configuration omits them. Logfire keeps the stored samples for nil.
func gatewayGuardrailSampleTextsToAPI(ctx context.Context, config tfsdk.Config, planned types.List, diags *diag.Diagnostics) *[]string {
	var configured types.List
	diags.Append(config.GetAttribute(ctx, path.Root("sample_texts"), &configured)...)
	if diags.HasError() || configured.IsNull() || planned.IsNull() || planned.IsUnknown() {
		return nil
	}
	out := []string{}
	diags.Append(planned.ElementsAs(ctx, &out, false)...)
	return &out
}

func gatewayGuardrailSampleTextsFromAPI(ctx context.Context, samples []string, diags *diag.Diagnostics) types.List {
	if samples == nil {
		samples = []string{}
	}
	list, d := types.ListValueFrom(ctx, types.StringType, samples)
	diags.Append(d...)
	return list
}

// importGatewayGuardrailByIDOrName imports by UUID, or looks up the
// organization-unique name through the list route and imports its UUID.
func importGatewayGuardrailByIDOrName(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, noun, example string, find func(context.Context, string) (string, error)) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError("Missing import ID", "Expected the "+noun+" UUID or its name. Example: "+example)
		return
	}
	if !uuidPattern.MatchString(id) {
		found, err := find(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Import "+noun+" failed", err.Error())
			return
		}
		id = found
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
