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

func TestDashboardManagerNamesTheTool(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool       string
		allowEdits bool
		want       string
	}{
		{"", true, `{"name":"Main","slug":"main","definition":{},"managed_by":"terraform","managed_allow_edits":true}`},
		{"Pulumi", false, `{"name":"Main","slug":"main","definition":{},"managed_by":"pulumi"}`},
		{"Crossplane", true, `{"name":"Main","slug":"main","definition":{},"managed_by":"other","managed_by_name":"Crossplane","managed_allow_edits":true}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			client, err := NewAPIClient("http://localhost", "test", nil, WithDashboardManager(tc.tool))
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(DashboardCreateRequest{
				Name: "Main", Slug: "main", Definition: json.RawMessage(`{}`),
				DashboardManager: client.DashboardManager(tc.allowEdits),
			})
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tc.want {
				t.Fatalf("create body = %s, want %s", body, tc.want)
			}
		})
	}
}
