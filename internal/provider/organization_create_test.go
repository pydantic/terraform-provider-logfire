// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestOrganizationCreateRetainsStateWhenBillingEmailFails(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api/v1/instance/organizations/":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","organization_name":"acme"}`))
		case "/api/oauth/token":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(organizationExchangeInvalidTarget))
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c, err := logclient.NewAPIClient(server.URL, "test-admin-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	if diags := plan.Set(t.Context(), &OrganizationModel{
		Name: types.StringValue("acme"), BillingEmail: types.StringValue("billing@example.com"),
		DeletionProtection: types.BoolValue(true),
	}); diags.HasError() {
		t.Fatal(diags)
	}
	response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("failed billing update must report an error")
	}
	if response.State.Raw.IsNull() {
		t.Fatal("failed billing update lost the created organization's state")
	}
	var state OrganizationModel
	if diags := response.State.Get(t.Context(), &state); diags.HasError() {
		t.Fatal(diags)
	}
	if state.ID.ValueString() != "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff" || state.Name.ValueString() != "acme" {
		t.Fatalf("partial create state id=%q name=%q, want the created organization", state.ID.ValueString(), state.Name.ValueString())
	}
	if !state.BillingEmail.IsNull() || !state.DeletionProtection.ValueBool() {
		t.Fatalf("partial create state billing_email=%v deletion_protection=%v", state.BillingEmail, state.DeletionProtection)
	}
}
