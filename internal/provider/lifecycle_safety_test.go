// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func lifecycleConfig(baseURL, kind, body string) string {
	return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\n}\nresource \"logfire_%s\" \"test\" {\n%s\n}", baseURL, kind, body)
}

const lifecycleWebhook = "config {\ntype = \"webhook\"\nurl = \"https://example.com/hook\"\nformat = \"auto\"\n}"

type lifecycleAPI struct {
	sync.Mutex
	item         map[string]any
	failFollowup bool
	creates      int
	deletes      int
}

// Only channel and organization recovery need a stateful local API. Ordinary
// resource lifecycles are covered by the live acceptance tests.
func newLifecycleAPI(t *testing.T, kind string) (*httptest.Server, *lifecycleAPI) {
	t.Helper()
	api := &lifecycleAPI{}
	collection, itemPath := "/api/v1/channels/", "/api/v1/channels/channel-1/"
	if kind == "organization" {
		collection, itemPath = "/api/v1/instance/organizations/", "/api/v1/organization/"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Lock()
		defer api.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-org-token","expires_in":900}`))
			return
		}
		if r.URL.Path != collection && r.URL.Path != itemPath {
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload map[string]any
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("request body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if config, ok := payload["config"].(map[string]any); ok && config["type"] == "slack-integration" {
				if _, present := config["include_agent_prompt"]; !present {
					config["include_agent_prompt"] = true
				}
			}
		}
		switch r.Method {
		case http.MethodPost:
			api.creates++
			api.item = map[string]any{"id": kind + "-1", "active": true}
			maps.Copy(api.item, payload)
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			if api.failFollowup {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"follow-up rejected"}`))
				return
			}
			maps.Copy(api.item, payload)
		case http.MethodDelete:
			api.deletes++
			api.item = nil
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet:
			if api.item == nil {
				if r.URL.Path == collection {
					_, _ = w.Write([]byte(`[]`))
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
				return
			}
		}
		out := maps.Clone(api.item)
		if config, ok := out["config"].(map[string]any); ok && config["type"] == "webhook" {
			masked := maps.Clone(config)
			masked["url"] = "https://example.com/**********"
			out["config"] = masked
		}
		if r.Method == http.MethodGet && r.URL.Path == collection {
			_ = json.NewEncoder(w).Encode([]map[string]any{out})
		} else {
			_ = json.NewEncoder(w).Encode(out)
		}
	}))
	t.Cleanup(server.Close)
	return server, api
}

func TestLifecycleSlackSettings(t *testing.T) {
	t.Parallel()
	server, api := newLifecycleAPI(t, "channel")
	slack := func(name, prompt string) string {
		return lifecycleConfig(server.URL, "channel", fmt.Sprintf("name = %q\nconfig {\ntype = \"slack-integration\"\ninstall_id = \"install-1\"\nchannel_id = \"C123\"\n%s\n}", name, prompt))
	}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: slack("slack", ""), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.include_agent_prompt", "true")},
			{Config: slack("slack", "include_agent_prompt = false"), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.include_agent_prompt", "false")},
			{
				Config: slack("renamed", ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("logfire_channel.test", "id", "channel-1"),
					resource.TestCheckResourceAttr("logfire_channel.test", "name", "renamed"),
					resource.TestCheckResourceAttr("logfire_channel.test", "active", "true"),
					resource.TestCheckResourceAttr("logfire_channel.test", "config.include_agent_prompt", "false")),
			},
			{Config: slack("renamed", ""), ResourceName: "logfire_channel.test", ImportState: true, ImportStateVerify: true},
			{Config: slack("renamed", "include_agent_prompt = true"), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.include_agent_prompt", "true")},
			{
				Config: lifecycleConfig(server.URL, "channel", "name = \"renamed\"\nactive = false\n"+lifecycleWebhook),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("logfire_channel.test", "active", "false"),
					resource.TestCheckResourceAttr("logfire_channel.test", "config.url", "https://example.com/hook")),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.creates != 1 || api.deletes != 1 || api.item != nil {
		t.Fatalf("channel lifecycle: creates=%d, deletes=%d, item=%v", api.creates, api.deletes, api.item)
	}
}

func TestLifecyclePartialCreateRecovery(t *testing.T) {
	for _, kind := range []string{"organization", "channel"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			server, api := newLifecycleAPI(t, kind)
			importID := "acme"
			body, attribute, expected := "name = \"acme\"\nbilling_email = \"billing@example.com\"", "billing_email", "billing@example.com"
			if kind == "channel" {
				importID = "channel-1"
				body, attribute, expected = "name = \"channel\"\nactive = false\n"+lifecycleWebhook, "active", "false"
			}
			config, address := lifecycleConfig(server.URL, kind, body), "logfire_"+kind+".test"
			api.failFollowup = true
			steps := []resource.TestStep{{Config: config, ExpectError: regexp.MustCompile("saved in state")}}
			if kind == "organization" {
				steps = append(steps, resource.TestStep{Config: config, ExpectError: regexp.MustCompile("Organization deletion is protected")})
			}
			steps = append(steps, resource.TestStep{
				Config:           fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\n}\nremoved {\nfrom = %s\nlifecycle { destroy = false }\n}", server.URL, address),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{partialCreateStateCheck{kind: kind}}},
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.failFollowup = false
				},
			})
			steps = append(steps, resource.TestStep{
				Config: config, ResourceName: address, ImportState: true, ImportStatePersist: true, ImportStateId: importID,
			})
			if kind == "organization" {
				config = lifecycleConfig(server.URL, kind, body+"\ndeletion_protection = false")
			}
			steps = append(steps, resource.TestStep{
				Config:           config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate)}},
				Check:            resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(address, "id", kind+"-1"), resource.TestCheckResourceAttr(address, attribute, expected)),
			})
			resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
			api.Lock()
			defer api.Unlock()
			if api.creates != 1 || api.deletes != 1 || api.item != nil {
				t.Fatalf("recovery must update the same resource, then destroy it: creates=%d, deletes=%d, item=%v", api.creates, api.deletes, api.item)
			}
		})
	}
}

type partialCreateStateCheck struct{ kind string }

func (check partialCreateStateCheck) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	if req.Plan.PriorState != nil && req.Plan.PriorState.Values != nil && req.Plan.PriorState.Values.RootModule != nil {
		for _, saved := range req.Plan.PriorState.Values.RootModule.Resources {
			if saved.Address != "logfire_"+check.kind+".test" {
				continue
			}
			if saved.AttributeValues["id"] != check.kind+"-1" || !saved.Tainted {
				resp.Error = fmt.Errorf("failed creation must retain the created ID and tainted state: %+v", saved)
			} else if check.kind == "organization" && saved.AttributeValues["deletion_protection"] != true {
				resp.Error = fmt.Errorf("failed creation lost deletion protection: %v", saved.AttributeValues)
			} else if check.kind == "channel" {
				config, ok := saved.AttributeValues["config"].(map[string]any)
				if !ok || config["url"] != "https://example.com/hook" {
					resp.Error = fmt.Errorf("failed creation lost the webhook secret: %v", saved.AttributeValues)
				}
			}
			return
		}
	}
	resp.Error = fmt.Errorf("failed creation lost resource state")
}

func TestLifecycleFailedCreates(t *testing.T) {
	const project = "project_id = \"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff\"\n"
	configs := map[string]string{
		"organization": "name = \"acme\"",
		"project":      "name = \"test\"",
		"channel":      "name = \"test\"\n" + lifecycleWebhook,
		"read_token":   project,
		"write_token":  project,
		"dashboard":    project + "name = \"test\"\nslug = \"test\"\ndefinition = jsonencode({kind = \"Dashboard\", metadata = {}, spec = {}})",
		"alert":        project + "name = \"test\"\nquery = \"SELECT 1\"\ntime_window = \"5m\"\nfrequency = \"1m\"\nnotify_when = \"has_matches\"\nchannel_assignments = []",
		"slo":          project + "scope_value = \"payments\"\nname = \"test\"\ntotal_query = \"true\"\nbad_query = \"false\"\ntarget_percent = \"99.9\"\nrolling_window = \"30d\"",
	}
	for kind, body := range configs {
		for _, status := range []int{503, 429, 0} {
			t.Run(fmt.Sprintf("%s/%d", kind, status), func(t *testing.T) {
				t.Parallel()
				var creates atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff/" {
						_, _ = w.Write([]byte(`{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","project_name":"production"}`))
						return
					}
					if r.Method != http.MethodPost {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					// Each received POST represents a committed creation whose response fails.
					creates.Add(1)
					if status == 0 {
						connection, _, err := http.NewResponseController(w).Hijack()
						if err != nil {
							t.Errorf("hijack response: %v", err)
							return
						}
						_ = connection.Close()
						return
					}
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"detail":"response lost after creation"}`))
				}))
				defer server.Close()
				resource.Test(t, resource.TestCase{
					IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
					Steps: []resource.TestStep{{Config: lifecycleConfig(server.URL, kind, body), ExpectError: regexp.MustCompile("failed")}},
				})
				if got := creates.Load(); got != 1 {
					t.Fatalf("uncertain response caused %d creations; want exactly one", got)
				}
			})
		}
	}
}

func TestLifecycleDashboardNullDefinition(t *testing.T) {
	t.Parallel()
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      lifecycleConfig("https://example.invalid", "dashboard", "project_id = \"project-1\"\nname = \"test\"\nslug = \"test\"\ndefinition = \"null\""),
			ExpectError: regexp.MustCompile("invalid dashboard definition"),
		}},
	})
}

func TestLifecycleProjectImportIDCollision(t *testing.T) {
	t.Parallel()
	const id = "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/":
			_, _ = fmt.Fprintf(w, `[{"id":"other","project_name":%q},{"id":%q,"project_name":"production","organization_name":"acme"}]`, id, id)
		case "/api/v1/projects/" + id + "/":
			_, _ = fmt.Fprintf(w, `{"id":%q,"project_name":"production","organization_name":"acme","visibility":"private"}`, id)
		default:
			t.Errorf("import resolved the wrong project: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:       lifecycleConfig(server.URL, "project", "name = \"production\""),
			ResourceName: "logfire_project.test", ImportState: true, ImportStateId: id,
			ImportStateCheck: func(states []*terraform.InstanceState) error {
				if len(states) != 1 || states[0].ID != id || states[0].Attributes["name"] != "production" {
					return fmt.Errorf("UUID import must select production, not the project named after its UUID: %v", states)
				}
				return nil
			},
		}},
	})
}
