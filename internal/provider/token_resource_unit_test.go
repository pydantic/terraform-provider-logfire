// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestTokenCreateMapsSuccessfulResponse(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		newResource func() resource.Resource
	}{
		{"read", NewReadTokenResource},
		{"write", NewWriteTokenResource},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"created-token-id","token":"created-token-value"}`))
			}))
			defer server.Close()
			c, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			r := tt.newResource()
			configurable, ok := r.(resource.ResourceWithConfigure)
			if !ok {
				t.Fatalf("%T does not support provider configuration", r)
			}
			var configured resource.ConfigureResponse
			configurable.Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &configured)
			if configured.Diagnostics.HasError() {
				t.Fatal(configured.Diagnostics)
			}
			var schemaResponse resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
			attributes := map[string]tftypes.Value{}
			for name, attribute := range schemaResponse.Schema.Attributes {
				attributes[name] = tftypes.NewValue(attribute.GetType().TerraformType(t.Context()), nil)
			}
			attributes["project_id"] = tftypes.NewValue(tftypes.String, "project-id")
			plan := tfsdk.Plan{Schema: schemaResponse.Schema, Raw: tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), attributes)}
			response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
			r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			for name, want := range map[string]string{"id": "created-token-id", "token": "created-token-value", "project_id": "project-id"} {
				var got types.String
				if diags := response.State.GetAttribute(t.Context(), path.Root(name), &got); diags.HasError() {
					t.Fatal(diags)
				}
				if got.ValueString() != want {
					t.Fatalf("%s = %q, want %q", name, got.ValueString(), want)
				}
			}
		})
	}
}
