// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// GatewayProxyScope is the scope id that carries gateway spend caps and the
// response-cache toggle via claims.
const GatewayProxyScope = "project:gateway_proxy"

// GatewaySettingsModel mirrors logclient.GatewayProxyClaims for the generic
// logfire_api_key resource's nested gateway block. Every field is optional;
// a null spending limit means no limit for that window and a null
// cache_enabled inherits the project default.
type GatewaySettingsModel struct {
	SpendingLimitDaily   types.Int64 `tfsdk:"spending_limit_daily"`
	SpendingLimitWeekly  types.Int64 `tfsdk:"spending_limit_weekly"`
	SpendingLimitMonthly types.Int64 `tfsdk:"spending_limit_monthly"`
	SpendingLimitTotal   types.Int64 `tfsdk:"spending_limit_total"`
	CacheEnabled         types.Bool  `tfsdk:"cache_enabled"`
}

// gatewaySettingsIsNull reports whether the whole gateway block is absent.
func gatewaySettingsIsNull(m *GatewaySettingsModel) bool {
	return m == nil
}

// gatewaySettingsIsEmpty reports whether a present block carries no values.
// Sending such a block to the API would be rejected as having no updatable
// claims, so callers must omit claims in this case.
func gatewaySettingsIsEmpty(m *GatewaySettingsModel) bool {
	if m == nil {
		return true
	}
	return m.SpendingLimitDaily.IsNull() &&
		m.SpendingLimitWeekly.IsNull() &&
		m.SpendingLimitMonthly.IsNull() &&
		m.SpendingLimitTotal.IsNull() &&
		m.CacheEnabled.IsNull()
}

// scopesContain reports whether the scope list contains the target scope.
func scopesContain(scopes []string, target string) bool {
	for _, s := range scopes {
		if s == target {
			return true
		}
	}
	return false
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
// deterministic create payload.
func sortedScopeStrings(set types.Set) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	elements := make([]string, 0, len(set.Elements()))
	for _, e := range set.Elements() {
		if s, ok := e.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			elements = append(elements, s.ValueString())
		}
	}
	sort.Strings(elements)
	return elements
}

// optionalStringPointer converts a Terraform string to a JSON pointer: null,
// unknown, or empty values become nil (field omitted).
func optionalStringPointer(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	trimmed := v.ValueString()
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// setOptionalStringFromPointer writes a JSON string pointer into a Terraform
// string, mapping nil to null.
func setOptionalStringFromPointer(target *types.String, value *string) {
	if value == nil || *value == "" {
		*target = types.StringNull()
	} else {
		*target = types.StringValue(*value)
	}
}

// gatewayClaimsForCreate builds the create-time claims payload from a gateway
// block. It returns nil when there is no block or the block is empty, so the
// request omits claims entirely.
func gatewayClaimsForCreate(m *GatewaySettingsModel) *logclient.ScopeClaims {
	if gatewaySettingsIsEmpty(m) {
		return nil
	}
	claims := &logclient.GatewayProxyClaims{}
	if !m.SpendingLimitDaily.IsNull() && !m.SpendingLimitDaily.IsUnknown() {
		v := int(m.SpendingLimitDaily.ValueInt64())
		claims.SpendingLimitDaily = &v
	}
	if !m.SpendingLimitWeekly.IsNull() && !m.SpendingLimitWeekly.IsUnknown() {
		v := int(m.SpendingLimitWeekly.ValueInt64())
		claims.SpendingLimitWeekly = &v
	}
	if !m.SpendingLimitMonthly.IsNull() && !m.SpendingLimitMonthly.IsUnknown() {
		v := int(m.SpendingLimitMonthly.ValueInt64())
		claims.SpendingLimitMonthly = &v
	}
	if !m.SpendingLimitTotal.IsNull() && !m.SpendingLimitTotal.IsUnknown() {
		v := int(m.SpendingLimitTotal.ValueInt64())
		claims.SpendingLimitTotal = &v
	}
	if !m.CacheEnabled.IsNull() && !m.CacheEnabled.IsUnknown() {
		v := m.CacheEnabled.ValueBool()
		claims.CacheEnabled = &v
	}
	return &logclient.ScopeClaims{GatewayProxy: claims}
}

// gatewayClaimsMapForUpdate builds the PATCH-merge claims map from a plan/state
// gateway block pair. Each field follows omit/unchanged, value/set,
// null/clear semantics. The second return value is false when there is nothing
// to send (block absent in plan, or present with no effective change), in
// which case the caller must omit claims to avoid the API's empty-patch 422.
func gatewayClaimsMapForUpdate(plan, state *GatewaySettingsModel) (map[string]any, bool) {
	if plan == nil {
		if gatewaySettingsIsEmpty(state) {
			return nil, false
		}
		// Removing the block clears every cap the key currently carries, so
		// the next refresh converges instead of diffing forever.
		inner := map[string]any{}
		clearInt := func(key string, s types.Int64) {
			if !s.IsNull() && !s.IsUnknown() {
				inner[key] = nil
			}
		}
		clearInt("spending_limit_daily", state.SpendingLimitDaily)
		clearInt("spending_limit_weekly", state.SpendingLimitWeekly)
		clearInt("spending_limit_monthly", state.SpendingLimitMonthly)
		clearInt("spending_limit_total", state.SpendingLimitTotal)
		if !state.CacheEnabled.IsNull() && !state.CacheEnabled.IsUnknown() {
			inner["cache_enabled"] = nil
		}
		if len(inner) == 0 {
			return nil, false
		}
		return map[string]any{GatewayProxyScope: inner}, true
	}
	inner := map[string]any{}
	mergeIntField := func(key string, p, s types.Int64) {
		switch {
		case p.IsUnknown():
			// Unknown: cannot decide, leave unchanged.
		case p.IsNull():
			if !s.IsNull() && !s.IsUnknown() {
				inner[key] = nil
			}
		default:
			if s.IsNull() || s.IsUnknown() || s.ValueInt64() != p.ValueInt64() {
				inner[key] = p.ValueInt64()
			}
		}
	}
	mergeIntField("spending_limit_daily", plan.SpendingLimitDaily, stateValueOrNull(state, func(m *GatewaySettingsModel) types.Int64 {
		return m.SpendingLimitDaily
	}))
	mergeIntField("spending_limit_weekly", plan.SpendingLimitWeekly, stateValueOrNull(state, func(m *GatewaySettingsModel) types.Int64 {
		return m.SpendingLimitWeekly
	}))
	mergeIntField("spending_limit_monthly", plan.SpendingLimitMonthly, stateValueOrNull(state, func(m *GatewaySettingsModel) types.Int64 {
		return m.SpendingLimitMonthly
	}))
	mergeIntField("spending_limit_total", plan.SpendingLimitTotal, stateValueOrNull(state, func(m *GatewaySettingsModel) types.Int64 {
		return m.SpendingLimitTotal
	}))
	var planCache, stateCache types.Bool
	planCache = plan.CacheEnabled
	if state != nil {
		stateCache = state.CacheEnabled
	} else {
		stateCache = types.BoolNull()
	}
	switch {
	case planCache.IsUnknown():
	case planCache.IsNull():
		if !stateCache.IsNull() && !stateCache.IsUnknown() {
			inner["cache_enabled"] = nil
		}
	default:
		if stateCache.IsNull() || stateCache.IsUnknown() || stateCache.ValueBool() != planCache.ValueBool() {
			inner["cache_enabled"] = planCache.ValueBool()
		}
	}
	if len(inner) == 0 {
		return nil, false
	}
	return map[string]any{GatewayProxyScope: inner}, true
}

// stateValueOrNull projects a field out of a possibly-nil state block.
func stateValueOrNull(state *GatewaySettingsModel, pick func(*GatewaySettingsModel) types.Int64) types.Int64 {
	if state == nil {
		return types.Int64Null()
	}
	return pick(state)
}

// gatewaySettingsFromClaims converts read-side claims into the Terraform
// model. It returns nil when the key carries no gateway-proxy settings, so
// the state keeps a null block unless the configuration sets one.
func gatewaySettingsFromClaims(claims logclient.ScopeClaims) *GatewaySettingsModel {
	g := claims.GatewayProxy
	if g == nil {
		return nil
	}
	m := &GatewaySettingsModel{
		SpendingLimitDaily:   types.Int64Null(),
		SpendingLimitWeekly:  types.Int64Null(),
		SpendingLimitMonthly: types.Int64Null(),
		SpendingLimitTotal:   types.Int64Null(),
		CacheEnabled:         types.BoolNull(),
	}
	if g.SpendingLimitDaily != nil {
		m.SpendingLimitDaily = types.Int64Value(int64(*g.SpendingLimitDaily))
	}
	if g.SpendingLimitWeekly != nil {
		m.SpendingLimitWeekly = types.Int64Value(int64(*g.SpendingLimitWeekly))
	}
	if g.SpendingLimitMonthly != nil {
		m.SpendingLimitMonthly = types.Int64Value(int64(*g.SpendingLimitMonthly))
	}
	if g.SpendingLimitTotal != nil {
		m.SpendingLimitTotal = types.Int64Value(int64(*g.SpendingLimitTotal))
	}
	if g.CacheEnabled != nil {
		m.CacheEnabled = types.BoolValue(*g.CacheEnabled)
	}
	return m
}
