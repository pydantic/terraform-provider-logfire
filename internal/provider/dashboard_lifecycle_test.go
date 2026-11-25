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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const dashboardLifecycleFile = `{
  "kind": "Dashboard",
  "metadata": {"name": "Copied", "project": "source", "version": 7, "layoutUpdatedAt": "old"},
  "spec": {
    "display": {"name": "Dashboard title"},
    "panels": {"text": {"kind": "Panel", "spec": {"display": {"name": "Text"}, "plugin": {"kind": "Markdown", "spec": {"text": "initial", "largeInteger": 9007199254740993, "number": 1e0, "fraction": 0.333333333333333333333333333333333333}}}}},
    "layouts": [{"kind": "Grid", "spec": {"display": {"title": "", "collapse": {"open": true}}, "items": [{"x": 0, "y": 0, "width": 12, "height": 4, "content": {"ref": "#/spec/panels/text"}}]}}],
    "variables": null,
    "annotations": [],
    "links": []
  }
}`

// A literal API response includes the defaults, aliases, and rounded fractions
// observed in platform. The fixture does not call the provider's normalizer.
const dashboardLifecycleAPI = `{"kind":"Dashboard","metadata":{"name":"Main","project":"test","version":8,"layoutUpdatedAt":"new"},"spec":{"display":{"name":"Dashboard title","description":null},"panels":{"text":{"kind":"Panel","spec":{"display":{"name":"Text"},"plugin":{"kind":"Markdown","spec":{"text":"initial","largeInteger":9007199254740993,"number":1.0,"fraction":0.3333333333333333}}}}},"layouts":[{"kind":"Grid","spec":{"items":[{"x":0,"y":0,"width":12,"height":4,"content":{"$ref":"#/spec/panels/text"}}]}}],"variables":[],"datasources":{},"duration":"1h","refreshInterval":"0s"}}`

type dashboardLifecycleAPIState struct {
	sync.Mutex
	id, name, definition string
	managedBy            string
	allowEdits           bool
	olderLogfire         bool
	creates              int
	writes               []string
	queries              []string
	denyProjectRead      bool
}

func (a *dashboardLifecycleAPIState) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	a.Lock()
	defer a.Unlock()
	w.Header().Set("Content-Type", "application/json")
	const base = "/api/v1/projects/project-1/dashboards/"
	dashboard := func() any {
		d := map[string]any{"id": a.id, "project_id": "project-1", "dashboard_name": a.name, "dashboard_slug": "main", "definition": json.RawMessage(a.definition)}
		if a.managedBy != "" {
			d["managed_by"], d["managed_allow_edits"] = a.managedBy, a.allowEdits
		}
		return d
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
			ManagedBy  string          `json:"managed_by"`
			AllowEdits bool            `json:"managed_allow_edits"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		a.writes = append(a.writes, string(body))
		a.queries = append(a.queries, req.URL.RawQuery)
		if payload.Name != nil {
			a.name = *payload.Name
		}
		if !a.olderLogfire {
			a.managedBy, a.allowEdits = payload.ManagedBy, payload.AllowEdits
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
		metadata := map[string]any{"name": a.name, "project": "test", "version": 8}
		if a.managedBy != "" {
			metadata["managedBy"], metadata["managedAllowEdits"] = a.managedBy, a.allowEdits
		}
		definition["metadata"], _ = json.Marshal(metadata)
		_ = json.NewEncoder(w).Encode(map[string]any{"dashboard": definition})
	case req.Method == http.MethodDelete && a.id != "" && req.URL.Path == base+a.id+"/":
		a.id = ""
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

const dashboardAddress = "logfire_dashboard.test"

func (a *dashboardLifecycleAPIState) mutate(change func()) func() {
	return func() {
		a.Lock()
		defer a.Unlock()
		change()
	}
}

func startDashboardLifecycleAPI(t *testing.T, api *dashboardLifecycleAPIState) string {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return server.URL
}

func writeDashboardFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dashboard.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func dashboardLifecycleConfig(baseURL, name, path, extra string) string {
	body := fmt.Sprintf("project_id = \"project-1\"\nname = %q\nslug = \"main\"\ndefinition = file(%q)\n%s", name, path, extra)
	return lifecycleConfig(baseURL, "dashboard", body)
}

func dashboardLifecycleRemoved(baseURL string) string {
	return fmt.Sprintf(`provider "logfire" {
  base_url = %q
  api_key = "test-token"
}
removed {
  from = logfire_dashboard.test
  lifecycle { destroy = false }
}`, baseURL)
}

func TestLifecycleDashboardJSONFile(t *testing.T) {
	t.Parallel()
	api := &dashboardLifecycleAPIState{}
	baseURL := startDashboardLifecycleAPI(t, api)
	withoutMetadata := strings.Replace(dashboardLifecycleFile, `  "metadata": {"name": "Copied", "project": "source", "version": 7, "layoutUpdatedAt": "old"},`+"\n", "", 1)
	withoutMetadata = strings.Replace(withoutMetadata, `"display": {"title": "", "collapse": {"open": true}}, `, "", 1)
	edited := strings.Replace(dashboardLifecycleFile, `"text": "initial"`, `"text": "edited"`, 1)
	reexportedAPI := `{"kind":"Dashboard","metadata":{"name":"Renamed","project":"test","version":8},"spec":{"display":{"name":"Dashboard title","description":null},"panels":{"text":{"kind":"Panel","spec":{"display":{"name":"Text"},"plugin":{"kind":"Markdown","spec":{"text":"edited","largeInteger":9007199254740992,"number":1.0,"fraction":0.3333333333333333}}}},"extra":{"kind":"Panel","spec":{"display":{"name":"Added in UI"},"plugin":{"kind":"Markdown","spec":{"text":"added in UI"}}}}},"layouts":[{"kind":"Grid","spec":{"items":[{"x":8,"y":4,"width":16,"height":6,"content":{"$ref":"#/spec/panels/text"}},{"x":0,"y":0,"width":8,"height":4,"content":{"$ref":"#/spec/panels/extra"}}]}}],"variables":[],"datasources":{},"duration":"1h","refreshInterval":"0s"}}`
	reexported := strings.Replace(reexportedAPI, `"layouts":[{"kind":"Grid","spec":{`, `"annotations":[],"links":[],"layouts":[{"kind":"Grid","spec":{"display":{"title":"","collapse":{"open":true}},`, 1)
	editedExport := strings.Replace(reexported, `"text":"added in UI"`, `"text":"edited in Terraform"`, 1)
	authored := writeDashboardFile(t, withoutMetadata)
	initial := writeDashboardFile(t, dashboardLifecycleFile)
	updated := writeDashboardFile(t, edited)
	config := func(name, path string) string {
		return dashboardLifecycleConfig(baseURL, name, path, "")
	}
	inPlace := accPreApply(plancheck.ExpectResourceAction(dashboardAddress, plancheck.ResourceActionUpdate))
	empty := accPreApply(plancheck.ExpectEmptyPlan())
	checkID := resource.TestCheckResourceAttr(dashboardAddress, "id", "dashboard-1")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("Main", authored),
				Check: resource.ComposeTestCheckFunc(
					checkID,
					resource.TestCheckResourceAttr(dashboardAddress, "definition", withoutMetadata)),
			},
			{Config: config("Main", initial), ConfigPlanChecks: empty},
			{Config: dashboardLifecycleRemoved(baseURL)},
			{
				Config: config("Main", initial), ResourceName: dashboardAddress,
				ImportState: true, ImportStateId: "test/main", ImportStatePersist: true,
			},
			{Config: config("Main", initial), ConfigPlanChecks: empty, Check: checkID},
			{
				Config: config("Renamed", initial), ConfigPlanChecks: inPlace,
				PreConfig: api.mutate(func() { api.denyProjectRead = true }),
				Check: resource.ComposeTestCheckFunc(
					checkID,
					resource.TestCheckResourceAttr(dashboardAddress, "name", "Renamed")),
			},
			{
				Config: config("Renamed", updated), ConfigPlanChecks: inPlace,
				PreConfig: api.mutate(func() { api.denyProjectRead = false }),
				Check: resource.ComposeTestCheckFunc(
					checkID,
					resource.TestCheckResourceAttr(dashboardAddress, "definition", edited)),
			},
			{Config: config("Renamed", updated), ConfigPlanChecks: empty},
			{
				Config: config("Renamed", updated), ConfigPlanChecks: inPlace,
				PreConfig: api.mutate(func() {
					api.name = "External name"
					api.definition = strings.Replace(api.definition, `"text":"edited"`, `"text":"external"`, 1)
				}),
				Check: resource.ComposeTestCheckFunc(
					checkID,
					resource.TestCheckResourceAttr(dashboardAddress, "name", "Renamed"),
					resource.TestCheckResourceAttr(dashboardAddress, "definition", edited)),
			},
			{Config: config("Renamed", updated), ConfigPlanChecks: empty},
			{
				Config: config("Renamed", updated), ConfigPlanChecks: empty,
				PreConfig: api.mutate(func() {
					api.definition = reexportedAPI
					if err := os.WriteFile(updated, []byte(reexported), 0600); err != nil {
						t.Fatal(err)
					}
				}),
				Check: resource.ComposeTestCheckFunc(
					checkID,
					resource.TestCheckResourceAttr(dashboardAddress, "definition", strings.Replace(reexportedAPI,
						`"metadata":{`, `"metadata":{"managedAllowEdits":false,"managedBy":"terraform",`, 1))),
			},
			{Config: config("Renamed", updated), ConfigPlanChecks: empty, Check: checkID},
			{
				Config: config("Renamed", updated), ConfigPlanChecks: inPlace,
				PreConfig: func() {
					if err := os.WriteFile(updated, []byte(editedExport), 0600); err != nil {
						t.Fatal(err)
					}
				},
				Check: resource.ComposeTestCheckFunc(
					checkID,
					resource.TestCheckResourceAttr(dashboardAddress, "definition", editedExport)),
			},
			{Config: config("Renamed", updated), ConfigPlanChecks: empty},
			{
				Config:    config("Main", initial),
				PreConfig: api.mutate(func() { api.id = "" }),
				ConfigPlanChecks: accPreApply(
					plancheck.ExpectResourceAction(dashboardAddress, plancheck.ResourceActionCreate),
				),
				Check: resource.TestCheckResourceAttr(dashboardAddress, "id", "dashboard-2"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.creates != 2 || api.id != "" {
		t.Fatalf("lifecycle left creates=%d, remote ID=%q; want two creates and final deletion", api.creates, api.id)
	}
	if len(api.writes) != 6 || api.writes[1] != `{"name":"Renamed","managed_by":"terraform"}` || !strings.HasSuffix(api.writes[0], `"managed_by":"terraform"}`) || !strings.Contains(api.writes[0], `"largeInteger":9007199254740993`) || !strings.Contains(api.writes[0], `"fraction":0.333333333333333333333333333333333333`) {
		t.Fatalf("unexpected dashboard writes: %v", api.writes)
	}
	for i, query := range []string{"", "", "replace_layout=true", "replace_layout=true", "replace_layout=true", ""} {
		if api.queries[i] != query {
			t.Fatalf("write %d query = %q, want %q", i, api.queries[i], query)
		}
	}
	for _, i := range []int{0, 5} {
		var payload struct {
			Definition struct {
				Metadata struct{ Name, Project string }
			}
		}
		if err := json.Unmarshal([]byte(api.writes[i]), &payload); err != nil {
			t.Fatal(err)
		}
		if metadata := payload.Definition.Metadata; metadata.Name != "Main" || metadata.Project != "test" {
			t.Fatalf("create %d metadata = %+v, want Main in test", i, metadata)
		}
	}
}

func TestLifecycleDashboardManagedAllowEdits(t *testing.T) {
	t.Parallel()
	api := &dashboardLifecycleAPIState{}
	baseURL := startDashboardLifecycleAPI(t, api)
	file := writeDashboardFile(t, dashboardLifecycleFile)
	config := func(extra string) string {
		return dashboardLifecycleConfig(baseURL, "Main", file, extra)
	}
	update := accPreApply(plancheck.ExpectResourceAction(dashboardAddress, plancheck.ResourceActionUpdate))
	empty := accPreApply(plancheck.ExpectEmptyPlan())
	allowed := resource.TestCheckResourceAttr(dashboardAddress, "managed_allow_edits", "true")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(""),
				Check:  resource.TestCheckResourceAttr(dashboardAddress, "managed_allow_edits", "false"),
			},
			{Config: config(""), ConfigPlanChecks: empty},
			{Config: config("managed_allow_edits = true"), Check: allowed},
			{Config: config(""), ConfigPlanChecks: empty, Check: allowed},
			{
				PreConfig:        api.mutate(func() { api.allowEdits = false }),
				Config:           config("managed_allow_edits = true"),
				ConfigPlanChecks: update,
				Check:            allowed,
			},
			{Config: dashboardLifecycleRemoved(baseURL)},
			{
				PreConfig: api.mutate(func() { api.managedBy = "" }),
				Config:    config(""), ResourceName: dashboardAddress,
				ImportState: true, ImportStateId: "test/main", ImportStatePersist: true,
			},
			{
				Config:           config(""),
				ConfigPlanChecks: empty,
				Check:            resource.TestCheckNoResourceAttr(dashboardAddress, "managed_allow_edits"),
			},
			{Config: config("managed_allow_edits = true"), ConfigPlanChecks: update, Check: allowed},
		},
	})
	api.Lock()
	defer api.Unlock()
	const allowEdits = `{"managed_by":"terraform","managed_allow_edits":true}`
	if len(api.writes) != 4 || !strings.HasSuffix(api.writes[0], `"managed_by":"terraform"}`) || api.writes[1] != allowEdits || api.writes[2] != allowEdits || api.writes[3] != allowEdits {
		t.Fatalf("unexpected dashboard writes: %v", api.writes)
	}
}

// A Logfire instance without managed dashboards reports no lock, so create and import agree on null.
func TestLifecycleDashboardOnOlderLogfire(t *testing.T) {
	t.Parallel()
	api := &dashboardLifecycleAPIState{olderLogfire: true}
	baseURL := startDashboardLifecycleAPI(t, api)
	config := dashboardLifecycleConfig(baseURL, "Main", writeDashboardFile(t, dashboardLifecycleFile), "")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckNoResourceAttr(dashboardAddress, "managed_allow_edits")},
			{
				Config: config, ResourceName: dashboardAddress, ImportState: true, ImportStateId: "test/main",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"definition"},
			},
		},
	})
}

// A lock that Logfire ignored stays a planned change, so the provider sets it once Logfire
// supports managed dashboards.
func TestLifecycleDashboardRetriesIgnoredLock(t *testing.T) {
	t.Parallel()
	api := &dashboardLifecycleAPIState{olderLogfire: true}
	baseURL := startDashboardLifecycleAPI(t, api)
	file := writeDashboardFile(t, dashboardLifecycleFile)
	config := dashboardLifecycleConfig(baseURL, "Main", file, "managed_allow_edits = false")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, ExpectNonEmptyPlan: true},
			{
				PreConfig: api.mutate(func() { api.olderLogfire = false }),
				Config:    config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(dashboardAddress, "managed_allow_edits", "false"),
					func(*terraform.State) error {
						api.Lock()
						defer api.Unlock()
						if api.managedBy == "" || api.allowEdits {
							return fmt.Errorf("dashboard is not locked: managed_by=%q managed_allow_edits=%v", api.managedBy, api.allowEdits)
						}
						return nil
					},
				),
			},
		},
	})
}
