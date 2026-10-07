// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
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
					switch {
					case req.URL.Path == "/api/v1/projects/project-1/dashboards/":
						_ = json.NewEncoder(w).Encode([]logclient.DashboardSummary{{ID: "replacement", DashboardSlug: "target"}})
					case req.URL.Path == "/api/v1/projects/project-1/":
						_, _ = w.Write([]byte(`{"project_name":"my-project"}`))
					case req.Method == http.MethodGet:
						_ = json.NewEncoder(w).Encode(logclient.GetDashboardResponse{Dashboard: json.RawMessage(`{"metadata":{},"spec":{}}`)})
					case req.Method == http.MethodPut:
						_ = json.NewEncoder(w).Encode(logclient.Dashboard{ID: "replacement", ProjectID: "project-1", DashboardName: "Updated", DashboardSlug: "target", Definition: json.RawMessage(`{"metadata":{},"spec":{}}`)})
					case req.Method == http.MethodDelete:
						w.WriteHeader(http.StatusNoContent)
					}
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

func TestDashboardLifecycleDoesNotAdoptReusedSlug(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"read", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/projects/project-1/":
					_, _ = w.Write([]byte(`{"project_name":"my-project"}`))
				case req.URL.Path == "/api/v1/projects/project-1/dashboards/old-dashboard/":
					w.WriteHeader(http.StatusNotFound)
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/projects/project-1/dashboards/":
					_ = json.NewEncoder(w).Encode([]logclient.DashboardSummary{{ID: "replacement", DashboardSlug: "target"}})
				default:
					t.Errorf("operation adopted another dashboard: %s %s", req.Method, req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			r := &DashboardResource{client: client}
			state := dashboardIdentityState(t, r, types.StringValue("old-dashboard"))
			got, diags := runDashboardLifecycleOperation(t, r, state, operation)
			if operation == "update" {
				if !diags.HasError() {
					t.Fatal("updating a deleted dashboard must fail")
				}
				return
			}
			if diags.HasError() {
				t.Fatal(diags)
			}
			if operation == "read" && !got.Raw.IsNull() {
				t.Fatal("read must remove the deleted dashboard from state")
			}
		})
	}
}
