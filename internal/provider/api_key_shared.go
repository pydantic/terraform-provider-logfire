// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// GatewayProxyScope is the scope id that carries gateway spend caps and the
// response-cache toggle via claims.
const GatewayProxyScope = "project:gateway_proxy"

// GatewaySettingsModel mirrors logclient.GatewayProxyClaims for the generic
// logfire_api_key resource's nested gateway block. Every field is optional;
// a null spending limit means no limit for that window and a null
// cache_enabled inherits the project default. The zero value is all null.
type GatewaySettingsModel struct {
	SpendingLimitDaily   types.Int64 `tfsdk:"spending_limit_daily"`
	SpendingLimitWeekly  types.Int64 `tfsdk:"spending_limit_weekly"`
	SpendingLimitMonthly types.Int64 `tfsdk:"spending_limit_monthly"`
	SpendingLimitTotal   types.Int64 `tfsdk:"spending_limit_total"`
	CacheEnabled         types.Bool  `tfsdk:"cache_enabled"`
}

// gatewaySettingsIsEmpty reports whether a present block carries no values.
// Sending such a block to the API would be rejected as having no updatable
// claims, so callers must omit claims in this case.
func gatewaySettingsIsEmpty(m *GatewaySettingsModel) bool {
	return m == nil ||
		m.SpendingLimitDaily.IsNull() &&
			m.SpendingLimitWeekly.IsNull() &&
			m.SpendingLimitMonthly.IsNull() &&
			m.SpendingLimitTotal.IsNull() &&
			m.CacheEnabled.IsNull()
}

// scopesRequiringProject returns the subset of scopes that must be bound to a
// specific project.
func scopesRequiringProject(scopes []string) []string {
	var out []string
	for _, s := range scopes {
		if logclient.IsProjectBoundScope(s) {
			out = append(out, s)
		}
	}
	return out
}

// sortedScopeStrings extracts and sorts the strings from a Terraform set for a
// deterministic create payload. Elements that are not known yet are skipped.
func sortedScopeStrings(set types.Set) []string {
	var scopes []string
	for _, e := range set.Elements() {
		if s, ok := e.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			scopes = append(scopes, s.ValueString())
		}
	}
	sort.Strings(scopes)
	return scopes
}

// optionalStringPointer converts a Terraform string to a JSON pointer: null
// and empty values become nil (field omitted).
func optionalStringPointer(v types.String) *string {
	s := v.ValueString()
	if s == "" {
		return nil
	}
	return &s
}

// stringOrNull maps a nil or empty JSON string to null.
func stringOrNull(value *string) types.String {
	if value == nil || *value == "" {
		return types.StringNull()
	}
	return types.StringValue(*value)
}

func intPointer(v types.Int64) *int {
	if v.IsNull() {
		return nil
	}
	i := int(v.ValueInt64())
	return &i
}

func intValue(p *int) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*p))
}

// gatewayClaimsForCreate builds the create-time claims payload from a gateway
// block. It returns nil when there is no block or the block is empty, so the
// request omits claims entirely.
func gatewayClaimsForCreate(m *GatewaySettingsModel) *logclient.ScopeClaims {
	if gatewaySettingsIsEmpty(m) {
		return nil
	}
	return &logclient.ScopeClaims{GatewayProxy: &logclient.GatewayProxyClaims{
		SpendingLimitDaily:   intPointer(m.SpendingLimitDaily),
		SpendingLimitWeekly:  intPointer(m.SpendingLimitWeekly),
		SpendingLimitMonthly: intPointer(m.SpendingLimitMonthly),
		SpendingLimitTotal:   intPointer(m.SpendingLimitTotal),
		CacheEnabled:         m.CacheEnabled.ValueBoolPointer(),
	}}
}

// gatewayClaimsMapForUpdate builds the PATCH-merge claims map from a plan/state
// gateway block pair. A nil block counts as all null, so removing the block
// clears every cap the key carries and the next refresh converges. Each field
// follows omit/unchanged, value/set, null/clear semantics. The second return
// value is false when there is nothing to send, in which case the caller must
// omit claims to avoid the API's empty-patch 422.
func gatewayClaimsMapForUpdate(plan, state *GatewaySettingsModel) (map[string]any, bool) {
	if plan == nil {
		plan = &GatewaySettingsModel{}
	}
	if state == nil {
		state = &GatewaySettingsModel{}
	}
	inner := map[string]any{}
	merge := func(key string, plan, state attr.Value, value any) {
		switch {
		case plan.Equal(state):
		case plan.IsNull():
			inner[key] = nil
		default:
			inner[key] = value
		}
	}
	merge("spending_limit_daily", plan.SpendingLimitDaily,
		state.SpendingLimitDaily, plan.SpendingLimitDaily.ValueInt64())
	merge("spending_limit_weekly", plan.SpendingLimitWeekly,
		state.SpendingLimitWeekly, plan.SpendingLimitWeekly.ValueInt64())
	merge("spending_limit_monthly", plan.SpendingLimitMonthly,
		state.SpendingLimitMonthly, plan.SpendingLimitMonthly.ValueInt64())
	merge("spending_limit_total", plan.SpendingLimitTotal,
		state.SpendingLimitTotal, plan.SpendingLimitTotal.ValueInt64())
	merge("cache_enabled", plan.CacheEnabled, state.CacheEnabled,
		plan.CacheEnabled.ValueBool())
	return map[string]any{GatewayProxyScope: inner}, len(inner) > 0
}

// gatewaySettingsFromClaims converts read-side claims into the Terraform
// model. The zero value, all null, stands for a key without gateway settings.
func gatewaySettingsFromClaims(g *logclient.GatewayProxyClaims) GatewaySettingsModel {
	if g == nil {
		return GatewaySettingsModel{}
	}
	return GatewaySettingsModel{
		SpendingLimitDaily:   intValue(g.SpendingLimitDaily),
		SpendingLimitWeekly:  intValue(g.SpendingLimitWeekly),
		SpendingLimitMonthly: intValue(g.SpendingLimitMonthly),
		SpendingLimitTotal:   intValue(g.SpendingLimitTotal),
		CacheEnabled:         types.BoolPointerValue(g.CacheEnabled),
	}
}

// apiKeyUpdate builds the PATCH for the in-place changes between plan and
// state. The second return value is false when nothing changed.
func apiKeyUpdate(plan, state *APIKeyModel) (logclient.APIKeyUpdate, bool) {
	var payload logclient.APIKeyUpdate
	if !plan.Name.Equal(state.Name) {
		payload.Name = logclient.NullableFieldValue(plan.Name.ValueString())
	}
	if !plan.Description.Equal(state.Description) {
		if plan.Description.IsNull() {
			payload.Description = logclient.NullableFieldNull[string]()
		} else {
			payload.Description = logclient.NullableFieldValue(
				plan.Description.ValueString())
		}
	}
	if claims, ok := gatewayClaimsMapForUpdate(plan.Gateway, state.Gateway); ok {
		payload.Claims = &claims
	}
	return payload, payload != (logclient.APIKeyUpdate{})
}

// apiKeyResult turns the outcome of a call that returns a key into the key to
// store. It returns nil when the call failed or the key is gone, after
// reporting the failure or dropping the key from state.
func apiKeyResult(ctx context.Context, read *logclient.APIKeyRead, err error, verb, label string, state *tfsdk.State, diags *diag.Diagnostics) *logclient.APIKeyRead {
	switch {
	case logclient.IsNotFoundError(err):
		state.RemoveResource(ctx)
	case err != nil:
		diags.AddError(verb+" "+label+" failed", err.Error())
	default:
		return read
	}
	return nil
}

// updateAPIKey patches the key when plan and state differ, and otherwise
// reads it back. It returns the key as stored, or nil as apiKeyResult does.
func updateAPIKey(ctx context.Context, c *logclient.APIClient, plan, state *APIKeyModel, label string, resp *resource.UpdateResponse) *logclient.APIKeyRead {
	id := state.ID.ValueString()
	payload, changed := apiKeyUpdate(plan, state)
	if !changed {
		read, err := c.GetAPIKey(ctx, id)
		return apiKeyResult(ctx, read, err, "Read", label, &resp.State,
			&resp.Diagnostics)
	}
	read, err := c.UpdateAPIKey(ctx, id, payload)
	return apiKeyResult(ctx, read, err, "Update", label, &resp.State,
		&resp.Diagnostics)
}
