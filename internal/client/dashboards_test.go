// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateDashboardLayoutReplacementIsOptIn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query string
		replace     bool
	}{
		{"preserve layout", "", false},
		{"replace layout", "replace_layout=true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
				}
				if req.Method != http.MethodPut || req.URL.Path != "/api/v1/projects/project-id/dashboards/dashboard-id/" || req.URL.RawQuery != tc.query {
					t.Errorf("unexpected update request: %s %s", req.Method, req.URL)
				}
				if string(body) != `{"definition":{"kind":"Dashboard","spec":{}}}` {
					t.Errorf("unexpected update body: %s", body)
				}
				_, _ = io.WriteString(w, `{"id":"dashboard-id","dashboard_name":"Main"}`)
			}))
			defer server.Close()
			client, err := NewAPIClient(server.URL, "test", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			definition := json.RawMessage(`{"kind":"Dashboard","spec":{}}`)
			out, err := client.UpdateDashboard(t.Context(), "project-id", "dashboard-id", DashboardUpdateRequest{Definition: &definition, ReplaceLayout: tc.replace})
			if err != nil {
				t.Fatal(err)
			}
			if out.ID != "dashboard-id" || out.DashboardName != "Main" {
				t.Fatalf("unexpected dashboard: %+v", out)
			}
		})
	}
}

func TestDashboardWritesSendManagedBy(t *testing.T) {
	t.Parallel()
	definition := json.RawMessage(`{"kind":"Dashboard","spec":{}}`)
	allow, deny := true, false
	for _, tc := range []struct {
		name, body string
		write      func(*APIClient) error
	}{
		{
			"create", `{"name":"Main","slug":"main","definition":{"kind":"Dashboard","spec":{}},"managed_by":"terraform"}`,
			func(c *APIClient) error {
				_, err := c.CreateDashboard(t.Context(), "project-id", DashboardCreateRequest{Name: "Main", Slug: "main", Definition: definition, ManagedBy: "terraform"})
				return err
			},
		},
		{
			"create with edits allowed", `{"name":"Main","slug":"main","definition":{"kind":"Dashboard","spec":{}},"managed_by":"other","managed_by_name":"crossplane","managed_allow_edits":true}`,
			func(c *APIClient) error {
				_, err := c.CreateDashboard(t.Context(), "project-id", DashboardCreateRequest{Name: "Main", Slug: "main", Definition: definition, ManagedBy: "other", ManagedByName: "crossplane", ManagedAllowEdits: true})
				return err
			},
		},
		{
			"update", `{"managed_by":"pulumi","managed_allow_edits":false}`,
			func(c *APIClient) error {
				_, err := c.UpdateDashboard(t.Context(), "project-id", "dashboard-id", DashboardUpdateRequest{ManagedBy: "pulumi", ManagedAllowEdits: &deny})
				return err
			},
		},
		{
			"update with edits allowed", `{"managed_by":"other","managed_by_name":"crossplane","managed_allow_edits":true}`,
			func(c *APIClient) error {
				_, err := c.UpdateDashboard(t.Context(), "project-id", "dashboard-id", DashboardUpdateRequest{ManagedBy: "other", ManagedByName: "crossplane", ManagedAllowEdits: &allow})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
				}
				if string(body) != tc.body {
					t.Errorf("unexpected body: %s", body)
				}
				// A server with the release gate off returns a null manager.
				_, _ = io.WriteString(w, `{"id":"dashboard-id","dashboard_name":"Main","managed_by":null}`)
			}))
			defer server.Close()
			client, err := NewAPIClient(server.URL, "test", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.write(client); err != nil {
				t.Fatal(err)
			}
		})
	}
}
