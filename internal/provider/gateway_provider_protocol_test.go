// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/pydantic/terraform-provider-logfire/internal/provider"
)

func TestGatewayProviderProtocolValidation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		attribute string
		value     any
		wantError bool
	}{
		{"valid", "vendor", "openai", false},
		{"unsupported vendor", "vendor", "azure", true},
		{"empty key", "api_key", "", true},
		{"missing key", "api_key", nil, true},
		{"invalid slug", "slug", "has/slash", true},
		{"unknown key", "api_key", tftypes.UnknownValue, false},
		{"unknown vendor", "vendor", tftypes.UnknownValue, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := providerserver.NewProtocol6(provider.New("test")())()
			_, s := gatewayResource(t, nil)
			values := map[string]any{"slug": "primary", "vendor": "openai", "api_key": "synthetic-key"}
			values[tt.attribute] = tt.value
			raw := gatewayValue(t, s, values)
			config, err := tfprotov6.NewDynamicValue(raw.Type(), raw)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.ValidateResourceConfig(t.Context(), &tfprotov6.ValidateResourceConfigRequest{
				TypeName: "logfire_gateway_provider", Config: &config,
			})
			if err != nil {
				t.Fatal(err)
			}
			hasError := false
			for _, diagnostic := range response.Diagnostics {
				hasError = hasError || diagnostic.Severity == tfprotov6.DiagnosticSeverityError
			}
			if hasError != tt.wantError {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
		})
	}
}

func TestGatewayProviderReplacementPlan(t *testing.T) {
	for _, tt := range []struct {
		attribute string
		value     any
		replace   bool
	}{
		{"slug", "replacement", true},
		{"vendor", "anthropic", true},
		{"api_key", "rotated-key", false},
		{"require_pricing", false, false},
	} {
		t.Run(tt.attribute, func(t *testing.T) {
			server := providerserver.NewProtocol6(provider.New("test")())()
			_, s := gatewayResource(t, nil)
			values := map[string]any{
				"id": "provider-id", "created_at": "2026-01-01T00:00:00Z", "slug": "primary",
				"vendor": "openai", "api_key": "synthetic-key", "require_pricing": true,
			}
			state := gatewayValue(t, s, values)
			prior, err := tfprotov6.NewDynamicValue(state.Type(), state)
			if err != nil {
				t.Fatal(err)
			}
			values[tt.attribute] = tt.value
			proposedRaw := gatewayValue(t, s, values)
			proposed, err := tfprotov6.NewDynamicValue(proposedRaw.Type(), proposedRaw)
			if err != nil {
				t.Fatal(err)
			}
			delete(values, "id")
			delete(values, "created_at")
			configRaw := gatewayValue(t, s, values)
			config, err := tfprotov6.NewDynamicValue(configRaw.Type(), configRaw)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
				TypeName: "logfire_gateway_provider", PriorState: &prior, Config: &config, ProposedNewState: &proposed,
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
