// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDashboardDefinitionComparison(t *testing.T) {
	minimal := types.StringValue(`{"kind":"Dashboard","metadata":{"name":"Main","project":"test"},"spec":{"display":{"name":"Main"},"panels":{},"layouts":[]}}`)
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"metadata including placement timestamp", `{"kind":"Dashboard","metadata":{"name":"Other","layoutUpdatedAt":"2026-10-14T10:00:00Z","extra":"ignored"},"spec":{"display":{"name":"Main"},"panels":{},"layouts":[]}}`, true},
		{"known API defaults", `{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"Main","description":null},"panels":{},"layouts":[],"datasources":{},"variables":[],"duration":"1h","refreshInterval":"0s"}}`, true},
		{"null variables", `{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"Main"},"panels":{},"layouts":[],"variables":null}}`, true},
		{"explicit null duration remains meaningful", `{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"Main"},"panels":{},"layouts":[],"duration":null}}`, false},
		{"explicit refresh remains meaningful", `{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"Main"},"panels":{},"layouts":[],"refreshInterval":"5s"}}`, false},
		{"unknown spec field remains meaningful", `{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"Main"},"panels":{},"layouts":[],"custom":false}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashboardDefinitionsEqual(minimal, types.StringValue(tc.raw)); got != tc.want {
				t.Fatalf("comparison = %v, want %v", got, tc.want)
			}
		})
	}
	datasource := types.StringValue(`{"kind":"Dashboard","spec":{"display":null,"panels":{},"layouts":[],"datasources":{"logs":{"display":{"name":"logs"},"plugin":{"kind":"Logfire","spec":{"number":9007199254740993}}}}}}`)
	expandedDatasource := types.StringValue(`{"kind":"Dashboard","spec":{"display":null,"panels":{},"layouts":[],"datasources":{"logs":{"display":{"name":"logs","description":null},"default":false,"plugin":{"kind":"Logfire","spec":{"number":9007199254740993}}}}}}`)
	if !dashboardDefinitionsEqual(datasource, expandedDatasource) {
		t.Fatal("datasource defaults must compare equal")
	}
	roundedDatasource := types.StringValue(strings.ReplaceAll(expandedDatasource.ValueString(), "9007199254740993", "9007199254740992"))
	if dashboardDefinitionsEqual(datasource, roundedDatasource) {
		t.Fatal("different large integers must remain different")
	}
	withPluginDefault := types.StringValue(strings.ReplaceAll(expandedDatasource.ValueString(), `"number":9007199254740993`, `"number":9007199254740993,"default":false`))
	if dashboardDefinitionsEqual(datasource, withPluginDefault) {
		t.Fatal("plugin JSON must not receive datasource defaults")
	}
	ref := types.StringValue(`{"kind":"Dashboard","spec":{"display":null,"panels":{},"layouts":[{"kind":"Grid","spec":{"items":[{"x":0,"y":0,"width":1,"height":1,"content":{"ref":"#/spec/panels/main"}}]}}]}}`)
	alias := types.StringValue(strings.ReplaceAll(ref.ValueString(), `"ref":`, `"$ref":`))
	if !dashboardDefinitionsEqual(ref, alias) {
		t.Fatal("PanelRef alias must compare equal at layout content")
	}
	if dashboardDefinitionsEqual(types.StringValue(`{"spec":{"plugin":{"ref":"one"}}}`), types.StringValue(`{"spec":{"plugin":{"$ref":"one"}}}`)) {
		t.Fatal("alias conversion must not affect arbitrary plugin JSON")
	}

}

func TestDashboardDefinitionValidator(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		wantError bool
	}{
		{`{"kind":"Dashboard","metadata":{},"spec":{}}`, false},
		{`null`, true}, {`[]`, true}, {`"text"`, true}, {`1`, true}, {`true`, true}, {`{`, true}, {`{} {}`, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			response := validator.StringResponse{}
			dashboardDefinitionValidator{}.ValidateString(t.Context(), validator.StringRequest{Path: path.Root("definition"), ConfigValue: types.StringValue(tc.raw)}, &response)
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
		})
	}
}

func TestDashboardDefinitionNumericComparison(t *testing.T) {
	hugeExponent := strings.Repeat("9", 4096)
	for _, tc := range []struct {
		name     string
		previous string
		current  string
		want     bool
	}{
		{"integer and decimal", `1`, `1.0`, true},
		{"integer and exponent", `1`, `1e0`, true},
		{"uppercase positive exponent", `1230`, `1.2300E+003`, true},
		{"leading exponent zeroes", `0.0012`, `12e-0004`, true},
		{"negative value", `-1200.00`, `-12e2`, true},
		{"negative zero", `-0.00e-5`, `0`, true},
		{"zero with huge exponent", `-0e` + hugeExponent, `0.000`, true},
		{"different integer signs", `-12`, `12`, false},
		{"large integer decimal", `9007199254740993`, `9007199254740993.0`, true},
		{"distinct large integers", `9007199254740993`, `9007199254740992`, false},
		{"high precision fraction", `0.123456789012345678901234567890`, `12345678901234567890123456789e-29`, true},
		{"distinct high precision fractions", `0.123456789012345678901234567890`, `0.123456789012345678901234567891`, false},
		{"huge positive exponent", `1e` + hugeExponent, `10e` + hugeExponent[:len(hugeExponent)-1] + `8`, true},
		{"huge negative exponent", `100e-` + hugeExponent, `1e-` + hugeExponent[:len(hugeExponent)-1] + `7`, true},
		{"distinct huge exponents", `1e` + hugeExponent, `1e` + hugeExponent[:len(hugeExponent)-1] + `8`, false},
		{"distinct values beyond float range", `1e10000`, `2e10000`, false},
		{"small value remains nonzero", `1e-10000`, `0`, false},
		{"string remains a string", `"1"`, `1`, false},
		{"numeric string spelling remains meaningful", `"1.0"`, `"1e0"`, false},
		{"nested plugin numbers and arrays", `{"plugin":{"values":[1,0.01,{"number":9007199254740993}],"query":"select 1.0"}}`, `{"plugin":{"values":[1.0,1e-2,{"number":9007199254740993e0}],"query":"select 1.0"}}`, true},
		{"nested number change", `{"plugin":{"values":[1,0.01,{"number":9007199254740993}]}}`, `{"plugin":{"values":[1.0,1e-2,{"number":9007199254740992}]}}`, false},
		{"array order remains meaningful", `[1,2]`, `[2.0,1.0]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := types.StringValue(`{"kind":"Dashboard","spec":{"plugin":` + tc.previous + `}}`)
			current := types.StringValue(`{"kind":"Dashboard","spec":{"plugin":` + tc.current + `}}`)
			if got := dashboardDefinitionsEqual(previous, current); got != tc.want {
				t.Fatalf("comparison = %v, want %v", got, tc.want)
			}
			if got := dashboardDefinitionsEqual(current, previous); got != tc.want {
				t.Fatalf("reverse comparison = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDashboardDefinitionPreservesNumberSpellings(t *testing.T) {
	raw := "{\n\"kind\":\"Dashboard\",\"spec\":{\"plugin\":{\"decimal\":1.000,\"integer\":9007199254740993,\"exponent\":123.450E+0006}}\n}"
	response := types.StringValue(`{"kind":"Dashboard","spec":{"plugin":{"decimal":1e0,"integer":9007199254740993.0,"exponent":123450000}}}`)
	if got := preserveDashboardDefinition(types.StringValue(raw), response); got.ValueString() != raw {
		t.Fatalf("equivalent numeric response changed the input JSON: %s", got.ValueString())
	}
	payload, err := dashboardDefinitionForWrite(raw, "Main", "test")
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"kind":"Dashboard","metadata":{"name":"Main","project":"test"},"spec":{"plugin":{"decimal":1.000,"exponent":123.450E+0006,"integer":9007199254740993}}}`
	if string(payload) != want {
		t.Fatalf("request JSON = %s, want %s", payload, want)
	}
}
