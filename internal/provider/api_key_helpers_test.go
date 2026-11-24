// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func nullGatewayBlock() *GatewaySettingsModel {
	return &GatewaySettingsModel{
		SpendingLimitDaily:   types.Int64Null(),
		SpendingLimitWeekly:  types.Int64Null(),
		SpendingLimitMonthly: types.Int64Null(),
		SpendingLimitTotal:   types.Int64Null(),
		CacheEnabled:         types.BoolNull(),
	}
}

func TestScopesRequiringProject(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		scopes []string
		want   int
	}{
		{name: "otlp write", scopes: []string{"project:write_otlp"}, want: 1},
		{name: "otlp read", scopes: []string{"project:read_otlp"}, want: 1},
		{name: "gateway", scopes: []string{"project:gateway_proxy"}, want: 1},
		{name: "management", scopes: []string{"organization:read_api_key", "project:read"}, want: 0},
		{name: "mixed", scopes: []string{"project:read", "project:write_otlp"}, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := len(scopesRequiringProject(tc.scopes)); got != tc.want {
				t.Fatalf("scopesRequiringProject(%v) count = %d, want %d", tc.scopes, got, tc.want)
			}
		})
	}
}

func TestGatewayClaimsForCreate(t *testing.T) {
	t.Parallel()
	if claims := gatewayClaimsForCreate(nil); claims != nil {
		t.Fatal("nil block must omit claims")
	}
	if claims := gatewayClaimsForCreate(nullGatewayBlock()); claims != nil {
		t.Fatal("empty block must omit claims")
	}
	block := nullGatewayBlock()
	block.SpendingLimitDaily = types.Int64Value(25)
	block.CacheEnabled = types.BoolValue(false)
	claims := gatewayClaimsForCreate(block)
	if claims == nil || claims.GatewayProxy == nil {
		t.Fatal("expected gateway claims")
	}
	if claims.GatewayProxy.SpendingLimitDaily == nil || *claims.GatewayProxy.SpendingLimitDaily != 25 {
		t.Fatalf("daily = %+v", claims.GatewayProxy.SpendingLimitDaily)
	}
	if claims.GatewayProxy.SpendingLimitWeekly != nil {
		t.Fatalf("weekly should be omitted, got %+v", claims.GatewayProxy.SpendingLimitWeekly)
	}
	if claims.GatewayProxy.CacheEnabled == nil || *claims.GatewayProxy.CacheEnabled {
		t.Fatalf("cache_enabled = %+v", claims.GatewayProxy.CacheEnabled)
	}
}

func TestGatewayClaimsMapForUpdate(t *testing.T) {
	t.Parallel()
	changed := func(t *testing.T, plan, state *GatewaySettingsModel) map[string]any {
		t.Helper()
		claims, ok := gatewayClaimsMapForUpdate(plan, state)
		if !ok {
			t.Fatal("expected a claims update")
		}
		inner, ok := claims[GatewayProxyScope].(map[string]any)
		if !ok {
			t.Fatalf("claims = %v", claims)
		}
		return inner
	}

	t.Run("nil plan and nil state omits claims", func(t *testing.T) {
		t.Parallel()
		if _, ok := gatewayClaimsMapForUpdate(nil, nil); ok {
			t.Fatal("expected no claims update")
		}
	})

	t.Run("removed block clears carried caps", func(t *testing.T) {
		t.Parallel()
		state := nullGatewayBlock()
		state.SpendingLimitDaily = types.Int64Value(10)
		state.CacheEnabled = types.BoolValue(true)
		inner := changed(t, nil, state)
		if v, present := inner["spending_limit_daily"]; !present || v != nil {
			t.Fatalf("daily should clear to null, got %v", inner)
		}
		if v, present := inner["cache_enabled"]; !present || v != nil {
			t.Fatalf("cache_enabled should clear to null, got %v", inner)
		}
		if _, present := inner["spending_limit_weekly"]; present {
			t.Fatalf("unset weekly must be omitted, got %v", inner)
		}
	})

	t.Run("unchanged values are omitted", func(t *testing.T) {
		t.Parallel()
		plan := nullGatewayBlock()
		plan.SpendingLimitDaily = types.Int64Value(10)
		state := nullGatewayBlock()
		state.SpendingLimitDaily = types.Int64Value(10)
		if _, ok := gatewayClaimsMapForUpdate(plan, state); ok {
			t.Fatal("no effective change must omit claims")
		}
	})

	t.Run("changed value is sent", func(t *testing.T) {
		t.Parallel()
		plan := nullGatewayBlock()
		plan.SpendingLimitDaily = types.Int64Value(20)
		state := nullGatewayBlock()
		state.SpendingLimitDaily = types.Int64Value(10)
		if inner := changed(t, plan, state); inner["spending_limit_daily"] != int64(20) {
			t.Fatalf("daily = %v", inner)
		}
	})

	t.Run("nulled field clears", func(t *testing.T) {
		t.Parallel()
		state := nullGatewayBlock()
		state.CacheEnabled = types.BoolValue(true)
		inner := changed(t, nullGatewayBlock(), state)
		if v, present := inner["cache_enabled"]; !present || v != nil {
			t.Fatalf("cache_enabled should clear to null, got %v", inner)
		}
	})
}

func TestAPIKeyCreateFromPlan(t *testing.T) {
	t.Parallel()
	scopes, diags := types.SetValueFrom(t.Context(), types.StringType, []string{"project:write_otlp"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	plan := &APIKeyModel{
		Name:        types.StringValue("otel"),
		Scopes:      scopes,
		ProjectID:   types.StringValue("33333333-3333-3333-3333-333333333333"),
		Description: types.StringNull(),
		ExpiresAt:   types.StringNull(),
	}
	in := apiKeyCreateFromPlan(plan)
	if in.Name != "otel" || len(in.Scopes) != 1 || in.Description != nil || in.ExpiresAt != nil || in.Claims != nil {
		t.Fatalf("unexpected create payload: %+v", in)
	}
	if in.ProjectID == nil || *in.ProjectID != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("project_id = %+v", in.ProjectID)
	}

	orgWide := &APIKeyModel{
		Name:      types.StringValue("mgmt"),
		Scopes:    scopes,
		ProjectID: types.StringNull(),
	}
	in = apiKeyCreateFromPlan(orgWide)
	if in.ProjectID != nil {
		t.Fatalf("org-wide key must omit project_id, got %v", *in.ProjectID)
	}

	padded := &APIKeyModel{
		Name:      types.StringValue("padded"),
		Scopes:    scopes,
		ExpiresAt: types.StringValue(" 2026-12-31T23:59:59Z "),
	}
	in = apiKeyCreateFromPlan(padded)
	if in.ExpiresAt == nil || *in.ExpiresAt != "2026-12-31T23:59:59Z" {
		t.Fatalf("expires_at = %+v, want it trimmed", in.ExpiresAt)
	}
}

func TestGatewayAPIKeyReadToStateRejectsNonGatewayKey(t *testing.T) {
	t.Parallel()
	read := &logclient.APIKeyRead{
		ID:          "11111111-1111-1111-1111-111111111111",
		Name:        "test",
		Scopes:      []string{"project:write_otlp"},
		AllProjects: true,
		Active:      true,
		CreatedAt:   "2026-01-01T00:00:00Z",
	}
	if _, err := gatewayAPIKeyToModel(read, types.StringNull()); err == nil {
		t.Fatal("expected a scope mismatch error")
	}
}
