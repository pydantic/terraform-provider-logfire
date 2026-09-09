// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/pydantic/terraform-provider-logfire/internal/provider"
)

func apiKeySchema(t *testing.T, typeName string) schema.Schema {
	t.Helper()
	var r resource.Resource
	switch typeName {
	case "logfire_api_key":
		r = provider.NewAPIKeyResource()
	case "logfire_gateway_api_key":
		r = provider.NewGatewayAPIKeyResource()
	default:
		t.Fatalf("unknown resource %q", typeName)
	}
	var response resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &response)
	return response.Schema
}

func apiKeyValue(t *testing.T, s schema.Schema, values map[string]any) tftypes.Value {
	t.Helper()
	attributeTypes := map[string]tftypes.Type{}
	attributes := map[string]tftypes.Value{}
	for name, attribute := range s.Attributes {
		typ := attribute.GetType().TerraformType(t.Context())
		attributeTypes[name] = typ
		v, ok := values[name]
		if !ok {
			v = nil
		}
		attributes[name] = tftypes.NewValue(typ, v)
	}
	return tftypes.NewValue(tftypes.Object{AttributeTypes: attributeTypes}, attributes)
}

func stringSetValue(t *testing.T, scopes ...string) []tftypes.Value {
	t.Helper()
	elements := make([]tftypes.Value, 0, len(scopes))
	for _, s := range scopes {
		elements = append(elements, tftypes.NewValue(tftypes.String, s))
	}
	return elements
}

func TestAPIKeyValidateConfig(t *testing.T) {
	t.Parallel()
	s := apiKeySchema(t, "logfire_api_key")
	server := providerserver.NewProtocol6(provider.New("test")())()
	for _, tt := range []struct {
		name    string
		values  map[string]any
		wantErr bool
	}{
		{
			name: "project otlp key with project",
			values: map[string]any{
				"name": "otel", "scopes": stringSetValue(t, "project:write_otlp"),
				"project_id": "33333333-3333-3333-3333-333333333333",
			},
		},
		{
			name: "org-wide management key",
			values: map[string]any{
				"name": "mgmt", "scopes": stringSetValue(t, "organization:read_api_key"),
			},
		},
		{
			name: "multi-scope otlp key with project",
			values: map[string]any{
				"name": "both", "scopes": stringSetValue(t, "project:read_otlp", "project:write_otlp"),
				"project_id": "33333333-3333-3333-3333-333333333333",
			},
		},
		{
			name: "project-bound scope without project",
			values: map[string]any{
				"name": "bad", "scopes": stringSetValue(t, "project:write_otlp"),
			},
			wantErr: true,
		},
		{
			name: "gateway scope without project",
			values: map[string]any{
				"name": "bad", "scopes": stringSetValue(t, "project:gateway_proxy"),
			},
			wantErr: true,
		},
		{
			name: "gateway block without gateway scope",
			values: map[string]any{
				"name": "bad", "scopes": stringSetValue(t, "project:write_otlp"),
				"project_id": "33333333-3333-3333-3333-333333333333",
				"gateway": map[string]tftypes.Value{
					"spending_limit_daily":   tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_weekly":  tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_monthly": tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_total":   tftypes.NewValue(tftypes.Number, nil),
					"cache_enabled":          tftypes.NewValue(tftypes.Bool, true),
				},
			},
			wantErr: true,
		},
		{
			name: "gateway block with gateway scope",
			values: map[string]any{
				"name": "gateway", "scopes": stringSetValue(t, "project:gateway_proxy"),
				"project_id": "33333333-3333-3333-3333-333333333333",
				"gateway": map[string]tftypes.Value{
					"spending_limit_daily":   tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_weekly":  tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_monthly": tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_total":   tftypes.NewValue(tftypes.Number, nil),
					"cache_enabled":          tftypes.NewValue(tftypes.Bool, true),
				},
			},
		},
		{
			name: "empty gateway block",
			values: map[string]any{
				"name": "gateway", "scopes": stringSetValue(t, "project:gateway_proxy"),
				"project_id": "33333333-3333-3333-3333-333333333333",
				"gateway": map[string]tftypes.Value{
					"spending_limit_daily":   tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_weekly":  tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_monthly": tftypes.NewValue(tftypes.Number, nil),
					"spending_limit_total":   tftypes.NewValue(tftypes.Number, nil),
					"cache_enabled":          tftypes.NewValue(tftypes.Bool, nil),
				},
			},
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := apiKeyValue(t, s, tt.values)
			config, err := tfprotov6.NewDynamicValue(raw.Type(), raw)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.ValidateResourceConfig(t.Context(), &tfprotov6.ValidateResourceConfigRequest{
				TypeName: "logfire_api_key", Config: &config,
			})
			if err != nil {
				t.Fatal(err)
			}
			hasError := false
			for _, diagnostic := range response.Diagnostics {
				hasError = hasError || diagnostic.Severity == tfprotov6.DiagnosticSeverityError
			}
			if hasError != tt.wantErr {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
		})
	}
}

func TestAPIKeyReplacementPlan(t *testing.T) {
	t.Parallel()
	s := apiKeySchema(t, "logfire_api_key")
	server := providerserver.NewProtocol6(provider.New("test")())()
	computed := map[string]any{
		"id": "11111111-1111-1111-1111-111111111111", "token": "secret",
		"project_name": "prod", "all_projects": false, "active": true,
		"created_at": "2026-01-01T00:00:00Z", "created_by_name": "Ada",
	}
	for _, tt := range []struct {
		attribute string
		value     any
		replace   bool
	}{
		{"name", "renamed", false},
		{"description", "docs", false},
		{"scopes", stringSetValue(t, "project:read_otlp"), true},
		{"project_id", "55555555-5555-5555-5555-555555555555", true},
		{"expires_at", "2027-01-01T00:00:00Z", true},
		{"gateway", map[string]tftypes.Value{
			"spending_limit_daily":   tftypes.NewValue(tftypes.Number, nil),
			"spending_limit_weekly":  tftypes.NewValue(tftypes.Number, nil),
			"spending_limit_monthly": tftypes.NewValue(tftypes.Number, nil),
			"spending_limit_total":   tftypes.NewValue(tftypes.Number, nil),
			"cache_enabled":          tftypes.NewValue(tftypes.Bool, true),
		}, false},
	} {
		t.Run(tt.attribute, func(t *testing.T) {
			t.Parallel()
			base := map[string]any{
				"name": "otel", "scopes": stringSetValue(t, "project:write_otlp"),
				"project_id": "33333333-3333-3333-3333-333333333333",
			}
			for k, v := range computed {
				base[k] = v
			}
			priorRaw := apiKeyValue(t, s, base)
			prior, err := tfprotov6.NewDynamicValue(priorRaw.Type(), priorRaw)
			if err != nil {
				t.Fatal(err)
			}
			base[tt.attribute] = tt.value
			proposedRaw := apiKeyValue(t, s, base)
			proposed, err := tfprotov6.NewDynamicValue(proposedRaw.Type(), proposedRaw)
			if err != nil {
				t.Fatal(err)
			}
			for k := range computed {
				delete(base, k)
			}
			configRaw := apiKeyValue(t, s, base)
			config, err := tfprotov6.NewDynamicValue(configRaw.Type(), configRaw)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
				TypeName: "logfire_api_key", PriorState: &prior, Config: &config, ProposedNewState: &proposed,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range response.Diagnostics {
				if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatal(diagnostic)
				}
			}
			if (len(response.RequiresReplace) > 0) != tt.replace {
				t.Fatalf("requires replacement = %v", response.RequiresReplace)
			}
		})
	}
}

func TestGatewayAPIKeyReplacementPlan(t *testing.T) {
	t.Parallel()
	s := apiKeySchema(t, "logfire_gateway_api_key")
	server := providerserver.NewProtocol6(provider.New("test")())()
	computed := map[string]any{
		"id": "11111111-1111-1111-1111-111111111111", "token": "secret",
		"project_name": "prod", "active": true, "created_at": "2026-01-01T00:00:00Z",
	}
	for _, tt := range []struct {
		attribute string
		value     any
		replace   bool
	}{
		{"name", "renamed", false},
		{"description", "docs", false},
		{"cache_enabled", true, false},
		{"project_id", "55555555-5555-5555-5555-555555555555", true},
		{"expires_at", "2027-01-01T00:00:00Z", true},
	} {
		t.Run(tt.attribute, func(t *testing.T) {
			t.Parallel()
			base := map[string]any{
				"name": "gateway", "project_id": "33333333-3333-3333-3333-333333333333",
			}
			for k, v := range computed {
				base[k] = v
			}
			priorRaw := apiKeyValue(t, s, base)
			prior, err := tfprotov6.NewDynamicValue(priorRaw.Type(), priorRaw)
			if err != nil {
				t.Fatal(err)
			}
			base[tt.attribute] = tt.value
			proposedRaw := apiKeyValue(t, s, base)
			proposed, err := tfprotov6.NewDynamicValue(proposedRaw.Type(), proposedRaw)
			if err != nil {
				t.Fatal(err)
			}
			for k := range computed {
				delete(base, k)
			}
			configRaw := apiKeyValue(t, s, base)
			config, err := tfprotov6.NewDynamicValue(configRaw.Type(), configRaw)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
				TypeName: "logfire_gateway_api_key", PriorState: &prior, Config: &config, ProposedNewState: &proposed,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range response.Diagnostics {
				if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatal(diagnostic)
				}
			}
			if (len(response.RequiresReplace) > 0) != tt.replace {
				t.Fatalf("requires replacement = %v", response.RequiresReplace)
			}
		})
	}
}
