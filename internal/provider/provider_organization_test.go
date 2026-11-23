// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"maps"
	"testing"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// configureProvider runs Configure with the given provider block values and
// returns the response.
func configureProvider(t *testing.T, values map[string]tftypes.Value) fwprovider.ConfigureResponse {
	t.Helper()
	var schemaResponse fwprovider.SchemaResponse
	(&LogfireProvider{}).Schema(t.Context(), fwprovider.SchemaRequest{}, &schemaResponse)
	attributes := map[string]tftypes.Value{}
	for name, attribute := range schemaResponse.Schema.Attributes {
		attributes[name] = tftypes.NewValue(attribute.GetType().TerraformType(t.Context()), nil)
	}
	attributes["base_url"] = tftypes.NewValue(tftypes.String, "https://logfire.example.invalid")
	attributes["api_key"] = tftypes.NewValue(tftypes.String, "admin-key")
	maps.Copy(attributes, values)
	config := tfsdk.Config{
		Schema: schemaResponse.Schema,
		Raw:    tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), attributes),
	}
	var response fwprovider.ConfigureResponse
	(&LogfireProvider{}).Configure(t.Context(), fwprovider.ConfigureRequest{Config: config}, &response)
	return response
}

func configuredOrganization(t *testing.T, response fwprovider.ConfigureResponse) string {
	t.Helper()
	if response.Diagnostics.HasError() {
		t.Fatalf("configure failed: %s", diagnosticsString(response.Diagnostics))
	}
	resources, ok := response.ResourceData.(*logclient.APIClient)
	if !ok {
		t.Fatalf("resources received %T, want *APIClient", response.ResourceData)
	}
	dataSources, ok := response.DataSourceData.(*logclient.APIClient)
	if !ok {
		t.Fatalf("data sources received %T, want *APIClient", response.DataSourceData)
	}
	if resources.Organization() != dataSources.Organization() {
		t.Fatalf("resources act in %q, data sources in %q", resources.Organization(), dataSources.Organization())
	}
	return resources.Organization()
}

func TestProviderOrganization(t *testing.T) {
	for name, tc := range map[string]struct {
		env    string
		config map[string]tftypes.Value
		want   string
	}{
		"not set":                  {want: ""},
		"config":                   {config: map[string]tftypes.Value{"organization": tftypes.NewValue(tftypes.String, "team-a")}, want: "team-a"},
		"environment":              {env: "team-env", want: "team-env"},
		"config wins over env":     {env: "team-env", config: map[string]tftypes.Value{"organization": tftypes.NewValue(tftypes.String, "team-a")}, want: "team-a"},
		"empty config unsets env":  {env: "team-env", config: map[string]tftypes.Value{"organization": tftypes.NewValue(tftypes.String, "")}, want: ""},
		"empty environment is off": {env: "", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LOGFIRE_ORGANIZATION", tc.env)
			if got := configuredOrganization(t, configureProvider(t, tc.config)); got != tc.want {
				t.Fatalf("organization = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProviderRejectsUnknownOrganization(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	response := configureProvider(t, map[string]tftypes.Value{
		"organization": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
	assertDiagnosticsContain(t, response.Diagnostics, "logfire_organization.x.name")
	if response.ResourceData != nil {
		t.Fatalf("configure handed out a client despite the unknown organization: %v", response.ResourceData)
	}
}

// The token exchange reads the audience <base_url>/<organization> as a path, so
// "team-a/" would act in team-a while state records "team-a/".
func TestProviderRejectsMalformedOrganization(t *testing.T) {
	for name, tc := range map[string]struct {
		env    string
		config map[string]tftypes.Value
	}{
		"config":      {config: map[string]tftypes.Value{"organization": tftypes.NewValue(tftypes.String, "team-a/")}},
		"environment": {env: "team-a/"},
		"path":        {env: "team-a/svc"},
		"uppercase":   {env: "Team-A"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LOGFIRE_ORGANIZATION", tc.env)
			response := configureProvider(t, tc.config)
			assertDiagnosticsContain(t, response.Diagnostics, "lowercase letters")
			if response.ResourceData != nil {
				t.Fatal("configure handed out a client for a malformed organization")
			}
		})
	}
}
