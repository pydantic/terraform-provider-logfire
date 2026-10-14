// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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
	mu              sync.Mutex
	id              string
	name            string
	definition      string
	creates         int
	writes          []string
	denyProjectRead bool
}

func (a *dashboardLifecycleAPIState) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	const base = "/api/v1/projects/project-1/dashboards/"
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
		if a.id == "" {
			_, _ = io.WriteString(w, `[]`)
		} else {
			write([]any{map[string]any{"id": a.id, "project_id": "project-1", "dashboard_name": a.name, "dashboard_slug": "main"}})
		}
	case req.Method == http.MethodPost && req.URL.Path == base:
		body, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		a.writes = append(a.writes, string(body))
		a.creates++
		a.id, a.name, a.definition = fmt.Sprintf("dashboard-%d", a.creates), "Main", dashboardLifecycleAPI
		w.WriteHeader(http.StatusCreated)
		write(a.dashboard())
	case a.id != "" && req.URL.Path == base+a.id+"/":
		switch req.Method {
		case http.MethodGet:
			write(map[string]any{"dashboard": json.RawMessage(a.definition)})
		case http.MethodPut:
			body, err := io.ReadAll(req.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			a.writes = append(a.writes, string(body))
			var update struct {
				Name       *string         `json:"name"`
				Definition json.RawMessage `json:"definition"`
			}
			if err := json.Unmarshal(body, &update); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if update.Name != nil {
				a.name = *update.Name
			}
			if update.Definition != nil {
				a.definition = string(update.Definition)
			}
			// The API owns the top-level metadata and returns the current name.
			var definition map[string]json.RawMessage
			_ = json.Unmarshal([]byte(a.definition), &definition)
			definition["metadata"], _ = json.Marshal(map[string]any{"name": a.name, "project": "test", "version": 9})
			encoded, _ := json.Marshal(definition)
			a.definition = string(encoded)
			write(a.dashboard())
		case http.MethodDelete:
			a.id = ""
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"detail":"not found"}`)
	}
}

func (a *dashboardLifecycleAPIState) dashboard() any {
	return map[string]any{"id": a.id, "project_id": "project-1", "dashboard_name": a.name, "dashboard_slug": "main", "definition": json.RawMessage(a.definition)}
}

type dashboardLifecycleProtocol struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	typ    tftypes.Type
}

func newDashboardLifecycleProtocol(t *testing.T, apiURL string) dashboardLifecycleProtocol {
	t.Helper()
	server := providerserver.NewProtocol6(New("test")())()
	schemas, err := server.GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	dashboardLifecycleDiagnostics(t, schemas.Diagnostics)
	p := dashboardLifecycleProtocol{t: t, server: server, typ: schemas.ResourceSchemas["logfire_dashboard"].ValueType()}
	providerType := schemas.Provider.ValueType()
	config := tftypes.NewValue(providerType, map[string]tftypes.Value{
		"base_url":       tftypes.NewValue(tftypes.String, apiURL),
		"api_key":        tftypes.NewValue(tftypes.String, "synthetic-test-token"),
		"custom_headers": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
	})
	dynamic, err := tfprotov6.NewDynamicValue(providerType, config)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := server.ConfigureProvider(t.Context(), &tfprotov6.ConfigureProviderRequest{Config: &dynamic})
	if err != nil {
		t.Fatal(err)
	}
	dashboardLifecycleDiagnostics(t, configured.Diagnostics)
	return p
}

func dashboardLifecycleDiagnostics(t *testing.T, diagnostics []*tfprotov6.Diagnostic) {
	t.Helper()
	for _, d := range diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("%s: %s", d.Summary, d.Detail)
		}
	}
}

func (p dashboardLifecycleProtocol) value(id any, name, definition string) *tfprotov6.DynamicValue {
	p.t.Helper()
	var values any
	if name != "" {
		values = map[string]tftypes.Value{
			"id": tftypes.NewValue(tftypes.String, id), "project_id": tftypes.NewValue(tftypes.String, "project-1"),
			"name": tftypes.NewValue(tftypes.String, name), "slug": tftypes.NewValue(tftypes.String, "main"),
			"definition": tftypes.NewValue(tftypes.String, definition),
		}
	}
	raw := tftypes.NewValue(p.typ, values)
	dynamic, err := tfprotov6.NewDynamicValue(p.typ, raw)
	if err != nil {
		p.t.Fatal(err)
	}
	return &dynamic
}

func (p dashboardLifecycleProtocol) raw(value *tfprotov6.DynamicValue) tftypes.Value {
	p.t.Helper()
	raw, err := value.Unmarshal(p.typ)
	if err != nil {
		p.t.Fatal(err)
	}
	return raw
}

func (p dashboardLifecycleProtocol) attribute(value *tfprotov6.DynamicValue, name string) string {
	p.t.Helper()
	var attributes map[string]tftypes.Value
	if err := p.raw(value).As(&attributes); err != nil {
		p.t.Fatal(err)
	}
	var result string
	if err := attributes[name].As(&result); err != nil {
		p.t.Fatal(err)
	}
	return result
}

func (p dashboardLifecycleProtocol) plan(prior *tfprotov6.DynamicValue, name, definition string, wantNoop bool) *tfprotov6.DynamicValue {
	p.t.Helper()
	id := any(tftypes.UnknownValue)
	if !p.raw(prior).IsNull() {
		id = p.attribute(prior, "id")
	}
	config := p.value(nil, name, definition)
	response, err := p.server.PlanResourceChange(p.t.Context(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: "logfire_dashboard", PriorState: prior, Config: config, ProposedNewState: p.value(id, name, definition),
	})
	if err != nil {
		p.t.Fatal(err)
	}
	dashboardLifecycleDiagnostics(p.t, response.Diagnostics)
	if len(response.RequiresReplace) != 0 {
		p.t.Fatalf("unexpected replacement: %v", response.RequiresReplace)
	}
	if noop := p.raw(prior).Equal(p.raw(response.PlannedState)); noop != wantNoop {
		p.t.Fatalf("empty plan = %v, want %v; planned state = %s", noop, wantNoop, p.raw(response.PlannedState))
	}
	return response.PlannedState
}

func (p dashboardLifecycleProtocol) apply(prior, planned *tfprotov6.DynamicValue, name, definition string) *tfprotov6.DynamicValue {
	p.t.Helper()
	response, err := p.server.ApplyResourceChange(p.t.Context(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName: "logfire_dashboard", PriorState: prior, PlannedState: planned, Config: p.value(nil, name, definition),
	})
	if err != nil {
		p.t.Fatal(err)
	}
	dashboardLifecycleDiagnostics(p.t, response.Diagnostics)
	if !p.raw(planned).IsNull() {
		for _, attribute := range []string{"name", "slug", "project_id", "definition"} {
			if got, want := p.attribute(response.NewState, attribute), p.attribute(planned, attribute); got != want {
				p.t.Fatalf("apply changed planned %s: got %q, want %q", attribute, got, want)
			}
		}
	}
	return response.NewState
}

func (p dashboardLifecycleProtocol) read(prior *tfprotov6.DynamicValue) *tfprotov6.DynamicValue {
	p.t.Helper()
	response, err := p.server.ReadResource(p.t.Context(), &tfprotov6.ReadResourceRequest{TypeName: "logfire_dashboard", CurrentState: prior})
	if err != nil {
		p.t.Fatal(err)
	}
	dashboardLifecycleDiagnostics(p.t, response.Diagnostics)
	return response.NewState
}

func dashboardLifecycleJSON(t *testing.T, got, want string) {
	t.Helper()
	decode := func(raw string) any {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(got), decode(want)) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestDashboardProtocolLifecycle(t *testing.T) {
	t.Parallel()
	api := &dashboardLifecycleAPIState{}
	httpServer := httptest.NewServer(api)
	defer httpServer.Close()
	p := newDashboardLifecycleProtocol(t, httpServer.URL)
	missing := p.value(nil, "", "")
	state := p.apply(missing, p.plan(missing, "Main", dashboardLifecycleFile, false), "Main", dashboardLifecycleFile)
	if id := p.attribute(state, "id"); id != "dashboard-1" {
		t.Fatalf("created ID = %q, want dashboard-1", id)
	}
	writtenDefinition := strings.NewReplacer(`"name": "Copied"`, `"name": "Main"`, `"project": "source"`, `"project": "test"`).Replace(dashboardLifecycleFile)
	dashboardLifecycleJSON(t, api.writes[0], `{"name":"Main","slug":"main","definition":`+writtenDefinition+`}`)
	state = p.read(state)
	p.plan(state, "Main", dashboardLifecycleFile, true)
	if definition := p.attribute(state, "definition"); definition != dashboardLifecycleFile {
		t.Fatalf("refresh did not retain file contents: %q", definition)
	}

	imported, err := p.server.ImportResourceState(t.Context(), &tfprotov6.ImportResourceStateRequest{TypeName: "logfire_dashboard", ID: "test/main"})
	if err != nil {
		t.Fatal(err)
	}
	dashboardLifecycleDiagnostics(t, imported.Diagnostics)
	if len(imported.ImportedResources) != 1 {
		t.Fatalf("imported resources = %d, want 1", len(imported.ImportedResources))
	}
	state = imported.ImportedResources[0].State
	dashboardLifecycleJSON(t, p.attribute(state, "definition"), dashboardLifecycleAPI)
	p.plan(p.read(state), "Main", dashboardLifecycleFile, true)

	api.mu.Lock()
	api.denyProjectRead = true
	api.mu.Unlock()
	state = p.apply(state, p.plan(state, "Renamed", dashboardLifecycleFile, false), "Renamed", dashboardLifecycleFile)
	dashboardLifecycleJSON(t, api.writes[1], `{"name":"Renamed"}`)
	api.mu.Lock()
	api.denyProjectRead = false
	api.mu.Unlock()
	if id := p.attribute(state, "id"); id != "dashboard-1" {
		t.Fatalf("name update ID = %q, want dashboard-1", id)
	}
	p.plan(p.read(state), "Renamed", dashboardLifecycleFile, true)

	updatedFile := strings.Replace(dashboardLifecycleFile, `"text": "initial"`, `"text": "edited"`, 1)
	state = p.apply(state, p.plan(state, "Renamed", updatedFile, false), "Renamed", updatedFile)
	updatedWrite := strings.NewReplacer(`"name": "Copied"`, `"name": "Renamed"`, `"project": "source"`, `"project": "test"`).Replace(updatedFile)
	dashboardLifecycleJSON(t, api.writes[2], `{"definition":`+updatedWrite+`}`)
	if id := p.attribute(state, "id"); id != "dashboard-1" {
		t.Fatalf("definition update ID = %q, want dashboard-1", id)
	}
	p.plan(p.read(state), "Renamed", updatedFile, true)

	// External edits must be visible after refresh and repaired by an in-place apply.
	api.mu.Lock()
	api.name = "External name"
	api.definition = strings.Replace(api.definition, `"name":"Renamed"`, `"name":"External name"`, 1)
	api.mu.Unlock()
	state = p.read(state)
	if name := p.attribute(state, "name"); name != "External name" {
		t.Fatalf("refreshed name = %q, want External name", name)
	}
	state = p.apply(state, p.plan(state, "Renamed", updatedFile, false), "Renamed", updatedFile)
	dashboardLifecycleJSON(t, api.writes[3], `{"name":"Renamed"}`)
	p.plan(p.read(state), "Renamed", updatedFile, true)

	api.mu.Lock()
	api.definition = strings.Replace(api.definition, `"text":"edited"`, `"text":"external"`, 1)
	api.mu.Unlock()
	state = p.read(state)
	if !strings.Contains(p.attribute(state, "definition"), `"text":"external"`) {
		t.Fatalf("refresh did not expose the remote definition: %s", p.attribute(state, "definition"))
	}
	state = p.apply(state, p.plan(state, "Renamed", updatedFile, false), "Renamed", updatedFile)
	dashboardLifecycleJSON(t, api.writes[4], `{"definition":`+updatedWrite+`}`)
	p.plan(p.read(state), "Renamed", updatedFile, true)

	// A one-unit difference above float64 precision must still produce a change.
	changedNumber := strings.Replace(updatedFile, "9007199254740993", "9007199254740992", 1)
	p.plan(state, "Renamed", changedNumber, false)

	api.mu.Lock()
	api.id = ""
	api.mu.Unlock()
	state = p.read(state)
	if !p.raw(state).IsNull() {
		t.Fatalf("deleted dashboard state = %s, want null", p.raw(state))
	}
	state = p.apply(state, p.plan(state, "Main", dashboardLifecycleFile, false), "Main", dashboardLifecycleFile)
	if id := p.attribute(state, "id"); id != "dashboard-2" {
		t.Fatalf("recreated ID = %q, want dashboard-2", id)
	}
	state = p.apply(state, missing, "", "")
	if !p.raw(state).IsNull() || api.id != "" {
		t.Fatalf("destroy left state %s and remote ID %q", p.raw(state), api.id)
	}
}

func TestDashboardProtocolReplacement(t *testing.T) {
	t.Parallel()
	for _, attribute := range []string{"slug", "project_id"} {
		t.Run(attribute, func(t *testing.T) {
			t.Parallel()
			p := newDashboardLifecycleProtocol(t, "https://example.test")
			prior := p.value("dashboard-1", "Main", dashboardLifecycleFile)
			var configValues, proposedValues map[string]tftypes.Value
			if err := p.raw(p.value(nil, "Main", dashboardLifecycleFile)).As(&configValues); err != nil {
				t.Fatal(err)
			}
			if err := p.raw(prior).As(&proposedValues); err != nil {
				t.Fatal(err)
			}
			configValues[attribute] = tftypes.NewValue(tftypes.String, "replacement")
			proposedValues[attribute] = configValues[attribute]
			config, err := tfprotov6.NewDynamicValue(p.typ, tftypes.NewValue(p.typ, configValues))
			if err != nil {
				t.Fatal(err)
			}
			proposed, err := tfprotov6.NewDynamicValue(p.typ, tftypes.NewValue(p.typ, proposedValues))
			if err != nil {
				t.Fatal(err)
			}
			response, err := p.server.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
				TypeName: "logfire_dashboard", PriorState: prior, Config: &config, ProposedNewState: &proposed,
			})
			if err != nil {
				t.Fatal(err)
			}
			dashboardLifecycleDiagnostics(t, response.Diagnostics)
			if len(response.RequiresReplace) != 1 || !response.RequiresReplace[0].Equal(tftypes.NewAttributePath().WithAttributeName(attribute)) {
				t.Fatalf("replacement paths = %v, want %s", response.RequiresReplace, attribute)
			}
			if got := p.attribute(response.PlannedState, attribute); got != "replacement" {
				t.Fatalf("planned %s = %q, want replacement", attribute, got)
			}
		})
	}
}
