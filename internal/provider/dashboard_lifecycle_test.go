// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

const dashboardLifecycleFile = `{
  "kind": "Dashboard",
  "metadata": {"name": "Copied", "project": "source", "version": 7, "layoutUpdatedAt": "old"},
  "spec": {
    "display": {"name": "Dashboard title"},
    "panels": {"text": {"kind": "Panel", "spec": {"display": {"name": "Text"}, "plugin": {"kind": "Markdown", "spec": {"text": "initial", "largeInteger": 9007199254740993, "number": 1e0}}}}},
    "layouts": [{"kind": "Grid", "spec": {"items": [{"x": 0, "y": 0, "width": 12, "height": 4, "content": {"ref": "#/spec/panels/text"}}]}}],
    "variables": null
  }
}`

// A literal API response includes the defaults, aliases, and numeric spellings
// observed in platform. The fixture does not call the provider's normalizer.
const dashboardLifecycleAPI = `{"kind":"Dashboard","metadata":{"name":"Main","project":"test","version":8,"layoutUpdatedAt":"new"},"spec":{"display":{"name":"Dashboard title","description":null},"panels":{"text":{"kind":"Panel","spec":{"display":{"name":"Text"},"plugin":{"kind":"Markdown","spec":{"text":"initial","largeInteger":9007199254740993,"number":1.0}}}}},"layouts":[{"kind":"Grid","spec":{"items":[{"x":0,"y":0,"width":12,"height":4,"content":{"$ref":"#/spec/panels/text"}}]}}],"variables":[],"datasources":{},"duration":"1h","refreshInterval":"0s"}}`

type dashboardLifecycleAPIState struct {
	sync.Mutex
	id, name, definition string
	creates              int
	writes               []string
	denyProjectRead      bool
}

func (a *dashboardLifecycleAPIState) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	a.Lock()
	defer a.Unlock()
	w.Header().Set("Content-Type", "application/json")
	const base = "/api/v1/projects/project-1/dashboards/"
	dashboard := func() any {
		return map[string]any{"id": a.id, "project_id": "project-1", "dashboard_name": a.name, "dashboard_slug": "main", "definition": json.RawMessage(a.definition)}
	}
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/api/v1/projects/":
		_, _ = io.WriteString(w, `[{"id":"project-1","project_name":"test"}]`)
	case req.Method == http.MethodGet && req.URL.Path == "/api/v1/projects/project-1/":
		if a.denyProjectRead {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"id":"project-1","project_name":"test"}`)
	case req.Method == http.MethodGet && req.URL.Path == base:
		summaries := []any{}
		if a.id != "" {
			summaries = append(summaries, dashboard())
		}
		_ = json.NewEncoder(w).Encode(summaries)
	case req.Method == http.MethodPost && req.URL.Path == base,
		req.Method == http.MethodPut && a.id != "" && req.URL.Path == base+a.id+"/":
		body, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload struct {
			Name       *string         `json:"name"`
			Definition json.RawMessage `json:"definition"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		a.writes = append(a.writes, string(body))
		if payload.Name != nil {
			a.name = *payload.Name
		}
		if req.Method == http.MethodPost {
			a.creates++
			a.id, a.definition = fmt.Sprintf("dashboard-%d", a.creates), dashboardLifecycleAPI
			w.WriteHeader(http.StatusCreated)
		} else if payload.Definition != nil {
			a.definition = string(payload.Definition)
		}
		_ = json.NewEncoder(w).Encode(dashboard())
	case req.Method == http.MethodGet && a.id != "" && req.URL.Path == base+a.id+"/":
		var definition map[string]json.RawMessage
		_ = json.Unmarshal([]byte(a.definition), &definition)
		definition["metadata"], _ = json.Marshal(map[string]any{"name": a.name, "project": "test", "version": 8})
		_ = json.NewEncoder(w).Encode(map[string]any{"dashboard": definition})
	case req.Method == http.MethodDelete && a.id != "" && req.URL.Path == base+a.id+"/":
		a.id = ""
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestLifecycleDashboardJSONFile(t *testing.T) {
	t.Parallel()
	api := &dashboardLifecycleAPIState{}
	server := httptest.NewServer(api)
	defer server.Close()
	initial, updated := filepath.Join(t.TempDir(), "initial.json"), filepath.Join(t.TempDir(), "updated.json")
	edited := strings.Replace(dashboardLifecycleFile, `"text": "initial"`, `"text": "edited"`, 1)
	for path, definition := range map[string]string{initial: dashboardLifecycleFile, updated: edited} {
		if err := os.WriteFile(path, []byte(definition), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := func(name, path string) string {
		return lifecycleConfig(server.URL, "dashboard", fmt.Sprintf("project_id = \"project-1\"\nname = %q\nslug = \"main\"\ndefinition = file(%q)", name, path))
	}
	const address = "logfire_dashboard.test"
	inPlace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate)}}
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("Main", initial),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(address, "id", "dashboard-1"),
					resource.TestCheckResourceAttr(address, "definition", dashboardLifecycleFile)),
			},
			{Config: config("Main", initial), ConfigPlanChecks: empty},
			{
				Config: fmt.Sprintf(`provider "logfire" {
  base_url = %q
  api_key = "test-token"
}
removed {
  from = logfire_dashboard.test
  lifecycle { destroy = false }
}`, server.URL),
			},
			{
				Config: config("Main", initial), ResourceName: address,
				ImportState: true, ImportStateId: "test/main", ImportStatePersist: true,
			},
			{
				Config: config("Main", initial), ConfigPlanChecks: empty,
				Check: resource.TestCheckResourceAttr(address, "id", "dashboard-1"),
			},
			{
				Config: config("Renamed", initial), ConfigPlanChecks: inPlace,
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.denyProjectRead = true
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(address, "id", "dashboard-1"),
					resource.TestCheckResourceAttr(address, "name", "Renamed")),
			},
			{
				Config: config("Renamed", updated), ConfigPlanChecks: inPlace,
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.denyProjectRead = false
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(address, "id", "dashboard-1"),
					resource.TestCheckResourceAttr(address, "definition", edited)),
			},
			{Config: config("Renamed", updated), ConfigPlanChecks: empty},
			{
				Config: config("Renamed", updated), ConfigPlanChecks: inPlace,
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.name = "External name"
					api.definition = strings.Replace(api.definition, `"text":"edited"`, `"text":"external"`, 1)
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(address, "id", "dashboard-1"),
					resource.TestCheckResourceAttr(address, "name", "Renamed"),
					resource.TestCheckResourceAttr(address, "definition", edited)),
			},
			{Config: config("Renamed", updated), ConfigPlanChecks: empty},
			{
				Config: config("Main", initial),
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.id = ""
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionCreate)}},
				Check:            resource.TestCheckResourceAttr(address, "id", "dashboard-2"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.creates != 2 || api.id != "" {
		t.Fatalf("lifecycle left creates=%d, remote ID=%q; want two creates and final deletion", api.creates, api.id)
	}
	if len(api.writes) != 5 || api.writes[1] != `{"name":"Renamed"}` || !strings.Contains(api.writes[0], `"largeInteger":9007199254740993`) {
		t.Fatalf("unexpected dashboard writes: %v", api.writes)
	}
}
