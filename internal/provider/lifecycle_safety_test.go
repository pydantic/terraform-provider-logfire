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
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
	"golang.org/x/net/idna"
)

func runLifecycleSteps(t *testing.T, steps []resource.TestStep) {
	t.Helper()
	resource.Test(t, resource.TestCase{
		IsUnitTest:               true,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

func lifecycleProvider(baseURL string) string {
	return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\n}\n", baseURL)
}

func lifecycleConfig(baseURL, kind, body string) string {
	return lifecycleProvider(baseURL) + fmt.Sprintf("resource \"logfire_%s\" \"test\" {\n%s\n}", kind, body)
}

// requireOneCreateAndDestroy fails unless the API saw exactly one create and one
// delete and holds no item, so the resource was created once and then destroyed.
func (api *lifecycleAPI) requireOneCreateAndDestroy(t *testing.T) {
	t.Helper()
	api.Lock()
	defer api.Unlock()
	if api.creates != 1 || api.deletes != 1 || api.item != nil {
		t.Fatalf("creates=%d, deletes=%d, item=%v; want one create, then its destroy", api.creates, api.deletes, api.item)
	}
}

const lifecycleWebhook = "config {\ntype = \"webhook\"\nurl = \"https://example.com/hook\"\nformat = \"auto\"\n}"

type lifecycleAPI struct {
	sync.Mutex
	item         map[string]any
	failFollowup bool
	// refuseCreates is the number of later creates that the public API rate limit refuses.
	refuseCreates int
	refusals      int
	creates       int
	deletes       int
	// unsubscribed is an address that left the channel. Logfire refuses a config that lists it.
	unsubscribed string
}

// normalizeEmail mimics the API's email-validator normalization for the spellings the tests use.
func normalizeEmail(address string) string {
	address = strings.TrimSpace(address)
	at := strings.LastIndex(address, "@")
	// UTS 46 maps the ideographic full stop to a dot.
	local, domain := address[:at], strings.ToLower(strings.ReplaceAll(address[at+1:], "\u3002", "."))
	if strings.EqualFold(local, "postmaster") {
		local = "postmaster"
	}
	if unicodeDomain, err := idna.ToUnicode(domain); err == nil {
		domain = unicodeDomain
	}
	return local + "@" + domain
}

// The channel and organization lifecycle tests need a stateful local API.
// Ordinary resource lifecycles are covered by the live acceptance tests.
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
			if _, sent := payload["billing_email"]; sent && kind == "organization" {
				t.Errorf("Logfire no longer accepts billing_email, but the provider sent it: %v", payload)
			}
			if config, ok := payload["config"].(map[string]any); ok && config["type"] == "email" {
				recipients, _ := config["recipients"].([]any)
				for i, recipient := range recipients {
					address, _ := recipient.(string)
					recipients[i] = normalizeEmail(address)
					if recipients[i] == api.unsubscribed {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"detail":"unsubscribed from this channel"}`))
						return
					}
				}
			}
			if config, ok := payload["config"].(map[string]any); ok && config["type"] == "slack-integration" {
				if _, present := config["include_agent_prompt"]; !present {
					config["include_agent_prompt"] = true
				}
			}
		}
		switch r.Method {
		case http.MethodPost:
			if api.refusals < api.refuseCreates {
				api.refusals++
				w.Header().Set("X-RateLimit-Resource", "public_api")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"detail":"Rate limit exceeded"}`))
				return
			}
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
	runLifecycleSteps(t, []resource.TestStep{
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
	})
	api.requireOneCreateAndDestroy(t)
}

func TestLifecycleOpsgenieBaseURL(t *testing.T) {
	t.Parallel()
	server, api := newLifecycleAPI(t, "channel")
	opsgenie := func(name, baseURL string) string {
		return lifecycleConfig(server.URL, "channel", fmt.Sprintf("name = %q\nconfig {\ntype = \"opsgenie\"\nauth_key = \"key\"\n%s\n}", name, baseURL))
	}
	const eu = "https://api.eu.opsgenie.com"
	runLifecycleSteps(t, []resource.TestStep{
		{Config: opsgenie("opsgenie", ""), Check: resource.TestCheckNoResourceAttr("logfire_channel.test", "config.base_url")},
		{
			PreConfig: func() {
				api.Lock()
				defer api.Unlock()
				config, ok := api.item["config"].(map[string]any)
				if !ok {
					t.Fatalf("stored config = %v", api.item["config"])
				}
				config["base_url"] = eu
			},
			Config: opsgenie("renamed", ""),
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("logfire_channel.test", "config.base_url", eu),
				func(*terraform.State) error {
					api.Lock()
					defer api.Unlock()
					config, _ := api.item["config"].(map[string]any)
					if got := config["base_url"]; got != eu {
						return fmt.Errorf("base_url after update = %v, want the value set outside Terraform", got)
					}
					return nil
				}),
		},
		{Config: opsgenie("renamed", "base_url = \"https://api.opsgenie.com \""), ExpectError: regexp.MustCompile(`without\s+spaces`)},
		{Config: opsgenie("renamed", "base_url = \"https://api.opsgenie.com\u0085\""), ExpectError: regexp.MustCompile(`without\s+spaces`)},
		{Config: opsgenie("renamed", "base_url = \"https://api.opsgenie.com\v\""), ExpectError: regexp.MustCompile(`without\s+spaces`)},
		{Config: opsgenie("renamed", "base_url = \"https://api.opsgenie.com\""), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.base_url", "https://api.opsgenie.com")},
	})
}

func TestLifecycleEmailRecipients(t *testing.T) {
	t.Parallel()
	server, api := newLifecycleAPI(t, "channel")
	named := func(name, recipients string) string {
		return lifecycleConfig(server.URL, "channel", fmt.Sprintf("name = %q\nconfig {\ntype = \"email\"\nrecipients = [%s]\n}", name, recipients))
	}
	email := func(recipients string) string { return named("email", recipients) }
	const normalized = `"a@Example.com", " Postmaster@example.com", "b@xn--bcher-kva.example\u00a0"`
	runLifecycleSteps(t, []resource.TestStep{
		{Config: email(`"a@example.com"`), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.recipients.#", "1")},
		{Config: email(`"a@example.com", "b@example.com"`), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.recipients.#", "2")},
		{Config: email(`"a@example.com", "b@example.com"`), ResourceName: "logfire_channel.test", ImportState: true, ImportStateVerify: true},
		// Logfire stores other spellings of these, so each must keep its configured spelling.
		{Config: email(normalized), Check: resource.TestCheckTypeSetElemAttr("logfire_channel.test", "config.recipients.*", " Postmaster@example.com")},
		{Config: email(normalized), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		{Config: email(`"not-an-address"`), ExpectError: regexp.MustCompile(`must be an email\s+address`)},
		{Config: email(`"a@example.com\u001f"`), ExpectError: regexp.MustCompile(`without\s+control\s+characters`)},
		{
			// Logfire keeps the last address after it unsubscribes, so a rename must not resend it.
			PreConfig: func() { api.Lock(); api.unsubscribed = "a@example.com"; api.Unlock() },
			Config:    named("renamed", normalized),
			Check:     resource.TestCheckResourceAttr("logfire_channel.test", "name", "renamed"),
		},
		{Config: named("renamed", `"a@example.com"`), ExpectError: regexp.MustCompile("unsubscribed")},
		// Logfire keeps the case of most mailbox names, so these are two recipients.
		{Config: named("renamed", `"Ops@example.com", "ops@example.com"`), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.recipients.#", "2")},
		{Config: named("renamed", `"Ops@example.com"`), Check: resource.TestCheckResourceAttr("logfire_channel.test", "config.recipients.#", "1")},
		{
			// A recipient changed outside Terraform to another mailbox case is drift.
			PreConfig: func() {
				api.Lock()
				defer api.Unlock()
				config, _ := api.item["config"].(map[string]any)
				config["recipients"] = []any{"ops@example.com"}
			},
			Config:   named("renamed", `"Ops@example.com"`),
			PlanOnly: true, ExpectNonEmptyPlan: true,
		},
		{Config: named("renamed", `"Ops@example.com"`), Check: resource.TestCheckTypeSetElemAttr("logfire_channel.test", "config.recipients.*", "Ops@example.com")},
	})
}

// Logfire maps U+3002 in a domain to a dot, so a create must keep the configured spelling.
func TestLifecycleEmailRecipientMappedDomain(t *testing.T) {
	t.Parallel()
	server, _ := newLifecycleAPI(t, "channel")
	config := lifecycleConfig(server.URL, "channel", "name = \"email\"\nconfig {\ntype = \"email\"\nrecipients = [\"oncall@example\u3002com\"]\n}")
	runLifecycleSteps(t, []resource.TestStep{
		{Config: config, Check: resource.TestCheckTypeSetElemAttr("logfire_channel.test", "config.recipients.*", "oncall@example\u3002com")},
		{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
	})
}

func TestLifecyclePartialCreateRecovery(t *testing.T) {
	t.Parallel()
	server, api := newLifecycleAPI(t, "channel")
	const address = "logfire_channel.test"
	config := lifecycleConfig(server.URL, "channel", "name = \"channel\"\nactive = false\n"+lifecycleWebhook)
	api.failFollowup = true
	runLifecycleSteps(t, []resource.TestStep{
		{Config: config, ExpectError: regexp.MustCompile("saved in state")},
		{
			Config:           lifecycleProvider(server.URL) + fmt.Sprintf("removed {\nfrom = %s\nlifecycle { destroy = false }\n}", address),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{partialCreateStateCheck{}}},
			PreConfig: func() {
				api.Lock()
				defer api.Unlock()
				api.failFollowup = false
			},
		},
		{Config: config, ResourceName: address, ImportState: true, ImportStatePersist: true, ImportStateId: "channel-1"},
		{
			Config:           config,
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate)}},
			Check:            resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(address, "id", "channel-1"), resource.TestCheckResourceAttr(address, "active", "false")),
		},
	})
	api.requireOneCreateAndDestroy(t)
}

// Logfire no longer stores billing_email. The provider must not send it, and a
// configured value must not cause an inconsistent result or drift.
func TestLifecycleOrganizationBillingEmail(t *testing.T) {
	t.Parallel()
	server, api := newLifecycleAPI(t, "organization")
	const address = "logfire_organization.test"
	protected := lifecycleConfig(server.URL, "organization", "name = \"acme\"\nbilling_email = \"billing@example.com\"")
	config := func(extra string) string {
		return lifecycleConfig(server.URL, "organization", "name = \"acme\"\ndeletion_protection = false\n"+extra)
	}
	emptyPlan := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	runLifecycleSteps(t, []resource.TestStep{
		{Config: protected, Check: resource.TestCheckResourceAttr(address, "billing_email", "billing@example.com")},
		{Config: protected, ConfigPlanChecks: emptyPlan},
		{
			Config:      lifecycleProvider(server.URL),
			ExpectError: regexp.MustCompile("Organization deletion is protected"),
		},
		{Config: config(`billing_email = "billing@example.com"`), Check: resource.TestCheckResourceAttr(address, "billing_email", "billing@example.com")},
		{Config: config(`billing_email = "finance@example.com"`), Check: resource.TestCheckResourceAttr(address, "billing_email", "finance@example.com")},
		{
			// This update reaches the API, which does not return billing_email.
			Config: config(`billing_email = "accounts@example.com"` + "\ndisplay_name = \"Acme\""),
			Check:  resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(address, "display_name", "Acme"), resource.TestCheckResourceAttr(address, "billing_email", "accounts@example.com")),
		},
		{
			// Removing the argument clears the value from state.
			Config: config(`display_name = "Acme"`),
			Check:  resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(address, "id", "organization-1"), resource.TestCheckNoResourceAttr(address, "billing_email")),
		},
		{Config: config(`display_name = "Acme"`), ConfigPlanChecks: emptyPlan},
		{Config: config(`display_name = "Acme"`), ResourceName: address, ImportState: true, ImportStateId: "acme", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"deletion_protection"}},
	})
	api.requireOneCreateAndDestroy(t)
}

// The public API rate limit refuses a request before the route runs, so the provider retries
// the create and the apply succeeds with one resource.
func TestLifecycleCreateAfterRateLimitRefusal(t *testing.T) {
	t.Parallel()
	server, api := newLifecycleAPI(t, "channel")
	api.refuseCreates = 2
	runLifecycleSteps(t, []resource.TestStep{{
		Config: lifecycleConfig(server.URL, "channel", "name = \"channel\"\n"+lifecycleWebhook),
		Check:  resource.TestCheckResourceAttr("logfire_channel.test", "id", "channel-1"),
	}})
	api.Lock()
	defer api.Unlock()
	if api.refusals != 2 || api.creates != 1 || api.deletes != 1 {
		t.Fatalf("refusals=%d, creates=%d, deletes=%d; want 2 refused creates, then one create and its destroy", api.refusals, api.creates, api.deletes)
	}
}

type partialCreateStateCheck struct{}

func (partialCreateStateCheck) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	if req.Plan.PriorState != nil && req.Plan.PriorState.Values != nil && req.Plan.PriorState.Values.RootModule != nil {
		for _, saved := range req.Plan.PriorState.Values.RootModule.Resources {
			if saved.Address != "logfire_channel.test" {
				continue
			}
			if saved.AttributeValues["id"] != "channel-1" || !saved.Tainted {
				resp.Error = fmt.Errorf("failed creation must retain the created ID and tainted state: %+v", saved)
			} else if config, ok := saved.AttributeValues["config"].(map[string]any); !ok || config["url"] != "https://example.com/hook" {
				resp.Error = fmt.Errorf("failed creation lost the webhook secret: %v", saved.AttributeValues)
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
				runLifecycleSteps(t, []resource.TestStep{{
					Config:      lifecycleConfig(server.URL, kind, body),
					ExpectError: regexp.MustCompile("failed"),
				}})
				if got := creates.Load(); got != 1 {
					t.Fatalf("uncertain response caused %d creations; want exactly one", got)
				}
			})
		}
	}
}

func TestLifecycleDashboardNullDefinition(t *testing.T) {
	t.Parallel()
	runLifecycleSteps(t, []resource.TestStep{{
		Config:      lifecycleConfig("https://example.invalid", "dashboard", "project_id = \"project-1\"\nname = \"test\"\nslug = \"test\"\ndefinition = \"null\""),
		ExpectError: regexp.MustCompile("dashboard definition must be a JSON object"),
	}})
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
	runLifecycleSteps(t, []resource.TestStep{{
		Config:       lifecycleConfig(server.URL, "project", "name = \"production\""),
		ResourceName: "logfire_project.test", ImportState: true, ImportStateId: id,
		ImportStateCheck: func(states []*terraform.InstanceState) error {
			if len(states) != 1 || states[0].ID != id || states[0].Attributes["name"] != "production" {
				return fmt.Errorf("UUID import must select production, not the project named after its UUID: %v", states)
			}
			return nil
		},
	}})
}

// Upjet observes a resource before Logfire assigns it an ID, so Read treats an
// empty ID as absent. Update and Delete refuse it. None of them calls the API.
func TestEmptyIDMakesNoRequests(t *testing.T) {
	resources := map[string]func() fwresource.Resource{
		"alert":                      func() fwresource.Resource { return &AlertResource{} },
		"api_key":                    func() fwresource.Resource { return &APIKeyResource{} },
		"channel":                    func() fwresource.Resource { return &ChannelResource{} },
		"dashboard":                  func() fwresource.Resource { return &DashboardResource{} },
		"gateway_api_key":            func() fwresource.Resource { return &GatewayAPIKeyResource{} },
		"project":                    func() fwresource.Resource { return &ProjectResource{} },
		"read_token":                 NewReadTokenResource,
		"schedule":                   func() fwresource.Resource { return &ScheduleResource{} },
		"slo":                        func() fwresource.Resource { return &SloResource{} },
		"write_token":                NewWriteTokenResource,
		"organization":               func() fwresource.Resource { return &OrganizationResource{} },
		"gateway_endpoint":           func() fwresource.Resource { return &GatewayEndpointResource{} },
		"gateway_provider":           func() fwresource.Resource { return &GatewayProviderResource{} },
		"frontend_application":       func() fwresource.Resource { return &FrontendApplicationResource{} },
		"frontend_application_token": func() fwresource.Resource { return &FrontendApplicationTokenResource{} },
	}
	for name, build := range resources {
		for _, op := range []string{"read", "update", "delete"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(server.Close)
				client, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
				if err != nil {
					t.Fatal(err)
				}
				r := build()
				configurable, ok := r.(fwresource.ResourceWithConfigure)
				if !ok {
					t.Fatalf("%s does not implement Configure", name)
				}
				configurable.Configure(t.Context(),
					fwresource.ConfigureRequest{ProviderData: client}, &fwresource.ConfigureResponse{})
				var schemaResp fwresource.SchemaResponse
				r.Schema(t.Context(), fwresource.SchemaRequest{}, &schemaResp)
				state := tfsdk.State{Schema: schemaResp.Schema,
					Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(t.Context()), nil)}
				diags := state.SetAttribute(t.Context(), path.Root("id"), "")
				if _, ok := schemaResp.Schema.Attributes["project_id"]; ok {
					diags.Append(state.SetAttribute(t.Context(), path.Root("project_id"), "project-1")...)
				}
				if diags.HasError() {
					t.Fatal(diags)
				}
				switch op {
				case "read":
					resp := fwresource.ReadResponse{State: state}
					r.Read(t.Context(), fwresource.ReadRequest{State: state}, &resp)
					if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
						t.Fatalf("read returned state=%v, diagnostics=%v", resp.State.Raw, resp.Diagnostics)
					}
				case "update":
					plan := tfsdk.Plan(state)
					resp := fwresource.UpdateResponse{State: state}
					r.Update(t.Context(), fwresource.UpdateRequest{Plan: plan, State: state}, &resp)
					diags = resp.Diagnostics
				case "delete":
					resp := fwresource.DeleteResponse{State: state}
					r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, &resp)
					diags = resp.Diagnostics
				}
				if requests.Load() != 0 {
					t.Fatalf("%s with an empty ID made %d API calls", op, requests.Load())
				}
				if op == "read" || (op == "update" && strings.HasSuffix(name, "_token") && !strings.HasPrefix(name, "frontend")) {
					return
				}
				if !diags.HasError() || diags.Errors()[0].Summary() != "Missing resource ID" {
					t.Fatalf("%s with an empty ID returned %v, want Missing resource ID", op, diags)
				}
			})
		}
	}
}

// Upjet can apply without running the config validators, so create rejects a
// non-RFC3339 expires_at before it sends a request.
func TestCreateRejectsInvalidExpiresAtWithoutRequest(t *testing.T) {
	resources := map[string]func() fwresource.Resource{
		"api_key":         func() fwresource.Resource { return &APIKeyResource{} },
		"gateway_api_key": func() fwresource.Resource { return &GatewayAPIKeyResource{} },
		"read_token":      NewReadTokenResource,
		"write_token":     NewWriteTokenResource,
	}
	for name, build := range resources {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			client, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			r := build()
			configurable, ok := r.(fwresource.ResourceWithConfigure)
			if !ok {
				t.Fatalf("%s does not implement Configure", name)
			}
			configurable.Configure(t.Context(),
				fwresource.ConfigureRequest{ProviderData: client}, &fwresource.ConfigureResponse{})
			var schemaResp fwresource.SchemaResponse
			r.Schema(t.Context(), fwresource.SchemaRequest{}, &schemaResp)
			plan := tfsdk.Plan{Schema: schemaResp.Schema,
				Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(t.Context()), nil)}
			diags := plan.SetAttribute(t.Context(), path.Root("expires_at"), "1800000000")
			diags.Append(plan.SetAttribute(t.Context(), path.Root("project_id"), "project-1")...)
			if diags.HasError() {
				t.Fatal(diags)
			}
			resp := fwresource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: plan.Raw.Copy()}}
			r.Create(t.Context(), fwresource.CreateRequest{Plan: plan}, &resp)
			if requests.Load() != 0 {
				t.Fatalf("create with an invalid expires_at made %d API calls", requests.Load())
			}
			if !resp.Diagnostics.HasError() || resp.Diagnostics[0].Summary() != "Invalid expires_at" {
				t.Fatalf("diagnostics = %v, want Invalid expires_at", resp.Diagnostics)
			}
		})
	}
}

// countingResource configures r with a client whose every request is counted
// and answered with body.
func countingResource(t *testing.T, r fwresource.ResourceWithConfigure, body string) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	r.Configure(t.Context(), fwresource.ConfigureRequest{ProviderData: client}, &fwresource.ConfigureResponse{})
	return &requests
}

func stateOf(t *testing.T, r fwresource.Resource, model any) tfsdk.State {
	t.Helper()
	var schemaResp fwresource.SchemaResponse
	r.Schema(t.Context(), fwresource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema,
		Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(t.Context()), nil)}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

// Deletion protection fails closed: only an explicit false allows a delete.
func TestOrganizationDeleteNeedsExplicitlyDisabledProtection(t *testing.T) {
	for name, protection := range map[string]types.Bool{
		"null": types.BoolNull(), "unknown": types.BoolUnknown(), "true": types.BoolValue(true),
	} {
		t.Run(name, func(t *testing.T) {
			r := &OrganizationResource{}
			requests := countingResource(t, r, `{}`)
			state := stateOf(t, r, OrganizationModel{
				ID: types.StringValue("org-1"), Name: types.StringValue("acme"), DeletionProtection: protection,
			})
			resp := fwresource.DeleteResponse{State: state}
			r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != "Organization deletion is protected" {
				t.Fatalf("diagnostics = %v, want the protection error", resp.Diagnostics)
			}
			if requests.Load() != 0 {
				t.Fatalf("protected delete made %d API calls", requests.Load())
			}
		})
	}
}

// A channel whose state lost its config block still updates instead of panicking.
func TestChannelUpdateWithoutPriorConfig(t *testing.T) {
	r := &ChannelResource{}
	countingResource(t, r, `{"id":"ch-1","organization_id":"o","label":"c","active":true,`+
		`"created_at":"2026-01-01T00:00:00Z","config":{"type":"webhook","format":"auto","url":"https://example.com/h"}}`)
	base := ChannelModel{ID: types.StringValue("ch-1"), Name: types.StringValue("c"), Active: types.BoolValue(true)}
	state := stateOf(t, r, base)
	base.Config = &ChannelConfigModel{
		Type: types.StringValue("webhook"), Format: types.StringValue("auto"), URL: types.StringValue("https://example.com/h"),
	}
	plan := tfsdk.Plan(stateOf(t, r, base))
	resp := fwresource.UpdateResponse{State: state}
	r.Update(t.Context(), fwresource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

// Upjet can apply without the config validators, so an invalid or
// out-of-range duration fails locally instead of being sent to the API.
func TestAlertRejectsInvalidDurationWithoutValidators(t *testing.T) {
	for _, tc := range []struct{ timeWindow, frequency, want string }{
		{"5mi", "1m", "Invalid duration"},
		{"1h", "0", "Duration out of range"},
		{"1h", "-1m", "Duration out of range"},
	} {
		_, diags := alertModelToCreate(t.Context(), &AlertModel{
			TimeWindow: newAlertDurationValue(tc.timeWindow), Frequency: newAlertDurationValue(tc.frequency),
		})
		if !diags.HasError() || diags.Errors()[0].Summary() != tc.want {
			t.Errorf("time_window=%q frequency=%q: diagnostics = %v, want %s", tc.timeWindow, tc.frequency, diags, tc.want)
		}
	}
}

// The config block is required only through a validator that Upjet can skip,
// so create and update refuse a missing one instead of panicking.
func TestChannelWithoutConfigIsRefused(t *testing.T) {
	r := &ChannelResource{}
	requests := countingResource(t, r, `{}`)
	model := ChannelModel{ID: types.StringValue("ch-1"), Name: types.StringValue("c"), Active: types.BoolValue(true)}
	state := stateOf(t, r, model)
	plan := tfsdk.Plan(state)
	var created fwresource.CreateResponse
	created.State = state
	r.Create(t.Context(), fwresource.CreateRequest{Plan: plan}, &created)
	updated := fwresource.UpdateResponse{State: state}
	r.Update(t.Context(), fwresource.UpdateRequest{Plan: plan, State: state}, &updated)
	for op, diags := range map[string]diag.Diagnostics{"create": created.Diagnostics, "update": updated.Diagnostics} {
		if !diags.HasError() || diags.Errors()[0].Summary() != "Missing channel config" {
			t.Errorf("%s: diagnostics = %v, want Missing channel config", op, diags)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("a channel without config made %d API calls", requests.Load())
	}
}
