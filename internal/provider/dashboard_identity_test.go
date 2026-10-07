// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func dashboardIdentityState(t *testing.T, r *DashboardResource, id types.String) tfsdk.State {
	t.Helper()
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema, Raw: tftypes.NewValue(schema.Schema.Type().TerraformType(t.Context()), nil)}
	m := DashboardModel{
		ID: id, ProjectID: types.StringValue("project-1"), Name: types.StringValue("Original"), Slug: types.StringValue("target"),
		Definition: newDefinitionStringValue(`{"kind":"Dashboard","metadata":{},"spec":{}}`),
	}
	if diags := state.Set(t.Context(), &m); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

func runDashboardLifecycleOperation(t *testing.T, r *DashboardResource, state tfsdk.State, operation string) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	switch operation {
	case "read":
		response := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
		return response.State, response.Diagnostics
	case "update":
		var m DashboardModel
		if diags := state.Get(t.Context(), &m); diags.HasError() {
			t.Fatal(diags)
		}
		m.Name = types.StringValue("Updated")
		plan := tfsdk.Plan(state)
		if diags := plan.Set(t.Context(), &m); diags.HasError() {
			t.Fatal(diags)
		}
		response := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{Plan: plan, State: state}, &response)
		return response.State, response.Diagnostics
	case "delete":
		response := resource.DeleteResponse{State: state}
		r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
		return response.State, response.Diagnostics
	default:
		t.Fatalf("unknown operation %q", operation)
		return state, nil
	}
}

// Crossplane observes before create with an absent ID. Terraform CLI cannot
// produce every such state, so keep these caller-specific resource checks.
func TestDashboardLifecycleWithMissingID(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"read", "update", "delete"} {
		for _, missing := range []struct {
			name string
			id   types.String
		}{
			{"null", types.StringNull()},
			{"empty", types.StringValue("")},
			{"unknown", types.StringUnknown()},
		} {
			t.Run(operation+"/"+missing.name, func(t *testing.T) {
				t.Parallel()
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					requests.Add(1)
					w.WriteHeader(http.StatusNotFound)
				}))
				defer server.Close()
				client, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
				if err != nil {
					t.Fatal(err)
				}
				r := &DashboardResource{client: client}
				state := dashboardIdentityState(t, r, missing.id)
				got, diags := runDashboardLifecycleOperation(t, r, state, operation)
				if operation == "read" {
					if diags.HasError() || !got.Raw.IsNull() || requests.Load() != 0 {
						t.Fatalf("observe-before-create read returned state=%v, diagnostics=%v, requests=%d", got.Raw, diags, requests.Load())
					}
					return
				}
				if !diags.HasError() {
					t.Fatal("missing dashboard ID must require an explicit import")
				}
				found := false
				for _, diagnostic := range diags {
					found = found || strings.Contains(diagnostic.Detail(), "Import the intended dashboard")
				}
				if !found {
					t.Fatalf("missing ID diagnostic lacks import recovery instructions: %v", diags)
				}
				if !got.Raw.Equal(state.Raw) {
					t.Fatal("missing ID operation must preserve state")
				}
				if requests.Load() != 0 {
					t.Fatalf("missing ID operation made %d API calls", requests.Load())
				}
			})
		}
	}
}
