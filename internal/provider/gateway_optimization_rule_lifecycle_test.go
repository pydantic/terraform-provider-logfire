// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	optimizationRuleTestID    = "018f45c0-3cab-7b2f-a8f7-8a0b55a7ee00"
	optimizationBuiltinRuleID = "018f45c0-3cab-7b2f-a8f7-8a0b55a7ee11"
	optimizationRulesURL      = "/api/v1/gateway/optimization-rules/"
)

type optimizationRuleAPI struct {
	sync.Mutex
	rules    map[string]*logclient.GatewayOptimizationRuleRead
	nextID   string
	requests map[string]int
	// bodies holds the raw JSON body of each write, in order.
	bodies []map[string]any
}

// newOptimizationRuleAPI mimics the public optimization rules API. Every write must carry the complete rule.
func newOptimizationRuleAPI(t *testing.T) (*httptest.Server, *optimizationRuleAPI) {
	t.Helper()
	api := &optimizationRuleAPI{
		rules: map[string]*logclient.GatewayOptimizationRuleRead{}, nextID: optimizationRuleTestID, requests: map[string]int{},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Lock()
		defer api.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-org-token","expires_in":900}`))
			return
		}
		route := r.Method + " " + r.URL.Path
		api.requests[route]++
		var payload logclient.GatewayOptimizationRuleWrite
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("%s body: %v", route, err)
			}
			for _, field := range []string{
				"name", "description", "category", "instruction", "message_role", "trigger_pattern",
				"priority", "failure_mode", "action", "enabled", "targets",
			} {
				if _, ok := body[field]; !ok {
					t.Errorf("%s body has no %q field: %s", route, field, raw)
				}
			}
			if targets, ok := body["targets"].([]any); !ok || targets == nil {
				t.Errorf("%s must send targets as a list: %s", route, raw)
			}
			api.bodies = append(api.bodies, body)
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&payload); err != nil {
				t.Errorf("%s body: %v", route, err)
			}
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, optimizationRulesURL), "/")
		switch {
		case r.Method == http.MethodPost && payload.TriggerPattern != nil && *payload.TriggerPattern == "uncheckable":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"detail":"The pattern could not be checked right now. Try saving again shortly."}`))
		case route == "POST "+optimizationRulesURL:
			for _, rule := range api.rules {
				if rule.Name == payload.Name {
					w.WriteHeader(http.StatusConflict)
					_, _ = fmt.Fprintf(w, `{"detail":"An optimization rule named %q already exists."}`, payload.Name)
					return
				}
			}
			rule := &logclient.GatewayOptimizationRuleRead{
				ID: api.nextID, Source: "user", CreatedAt: "2026-11-25T00:00:00Z", UpdatedAt: "2026-11-25T00:00:00Z",
			}
			applyOptimizationRuleWrite(rule, payload)
			api.rules[rule.ID] = rule
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(rule)
		case route == "GET "+optimizationRulesURL:
			page := logclient.GatewayOptimizationRulesPage{Rules: []logclient.GatewayOptimizationRuleRead{}}
			if limit := r.URL.Query().Get("limit"); limit != "1" {
				t.Errorf("name lookup sent limit=%q, want 1", limit)
			}
			for _, rule := range api.rules {
				if rule.Name == r.URL.Query().Get("name") {
					page.Rules = append(page.Rules, *rule)
				}
			}
			_ = json.NewEncoder(w).Encode(page)
		case api.rules[id] == nil && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case api.rules[id] == nil:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Optimization rule not found in this organization."}`))
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(api.rules[id])
		case r.Method == http.MethodPut:
			if changed := catalogRuleContentChanges(api.rules[id], payload); len(changed) > 0 {
				t.Errorf("the plan must refuse a catalog rule change of %v before apply", changed)
				w.WriteHeader(http.StatusConflict)
				_, _ = fmt.Fprintf(w, `{"detail":"The request changes: %s."}`, strings.Join(changed, ", "))
				return
			}
			applyOptimizationRuleWrite(api.rules[id], payload)
			api.rules[id].UpdatedAt = "2026-11-25T01:00:00Z"
			_ = json.NewEncoder(w).Encode(api.rules[id])
		case r.Method == http.MethodDelete:
			delete(api.rules, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected API request: %s", route)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, api
}

func applyOptimizationRuleWrite(rule *logclient.GatewayOptimizationRuleRead, in logclient.GatewayOptimizationRuleWrite) {
	rule.Name, rule.Description, rule.Category, rule.Instruction = in.Name, in.Description, in.Category, in.Instruction
	rule.MessageRole, rule.TriggerPattern, rule.Priority = in.MessageRole, in.TriggerPattern, in.Priority
	rule.FailureMode, rule.Action, rule.Enabled, rule.Targets = in.FailureMode, in.Action, in.Enabled, in.Targets
}

// catalogRuleContentChanges returns the fields other than enabled and targets that a write changes on a
// catalog rule. Logfire refuses such a write with a 409.
func catalogRuleContentChanges(rule *logclient.GatewayOptimizationRuleRead, in logclient.GatewayOptimizationRuleWrite) []string {
	if rule.Source != "builtin" {
		return nil
	}
	var changed []string
	for name, same := range map[string]bool{
		"name": rule.Name == in.Name, "description": reflect.DeepEqual(rule.Description, in.Description),
		"category": rule.Category == in.Category, "instruction": rule.Instruction == in.Instruction,
		"message_role": rule.MessageRole == in.MessageRole, "priority": rule.Priority == in.Priority,
		"trigger_pattern": reflect.DeepEqual(rule.TriggerPattern, in.TriggerPattern),
		"failure_mode":    rule.FailureMode == in.FailureMode, "action": rule.Action == in.Action,
	} {
		if !same {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

func optimizationRuleConfig(baseURL, body string) string {
	return lifecycleConfig(baseURL, "gateway_optimization_rule",
		"name = \"Concise answers\"\ncategory = \"verbosity\"\ninstruction = \"Answer in at most three sentences.\"\n"+body)
}

func TestLifecycleGatewayOptimizationRule(t *testing.T) {
	t.Parallel()
	server, api := newOptimizationRuleAPI(t)
	const resourceName = "logfire_gateway_optimization_rule.test"
	targets := `
description     = "Keep support answers short."
trigger_pattern = "(?i)summar"
priority        = 5
action          = "observe"
message_role    = "user"
targets = [
  { endpoint_slug = "support" },
  { endpoint_slug = "support", model_pattern = "gpt-5*", agent_name = "triage", system_prompt_pattern = "support" },
]`
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: optimizationRuleConfig(server.URL, ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", optimizationRuleTestID),
					resource.TestCheckResourceAttr(resourceName, "message_role", "system"),
					resource.TestCheckResourceAttr(resourceName, "priority", "100"),
					resource.TestCheckResourceAttr(resourceName, "failure_mode", "fail_open"),
					resource.TestCheckResourceAttr(resourceName, "action", "transform"),
					resource.TestCheckResourceAttr(resourceName, "enabled", "true"),
					resource.TestCheckResourceAttr(resourceName, "targets.#", "0"),
					resource.TestCheckResourceAttr(resourceName, "source", "user"),
					resource.TestCheckNoResourceAttr(resourceName, "template_slug"),
					resource.TestCheckNoResourceAttr(resourceName, "description"),
					resource.TestCheckNoResourceAttr(resourceName, "trigger_pattern"),
					resource.TestCheckResourceAttr(resourceName, "created_at", "2026-11-25T00:00:00Z"),
				),
			},
			{
				Config: optimizationRuleConfig(server.URL, targets),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "description", "Keep support answers short."),
					resource.TestCheckResourceAttr(resourceName, "priority", "5"),
					resource.TestCheckResourceAttr(resourceName, "message_role", "user"),
					resource.TestCheckResourceAttr(resourceName, "targets.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs(resourceName, "targets.*", map[string]string{
						"endpoint_slug": "support", "model_pattern": "gpt-5*", "agent_name": "triage",
						"system_prompt_pattern": "support",
					}),
				),
			},
			{
				Config: optimizationRuleConfig(server.URL, targets), ResourceName: resourceName,
				ImportState: true, ImportStateId: "Concise answers", ImportStateVerify: true,
			},
			{
				Config: optimizationRuleConfig(server.URL, targets), ResourceName: resourceName,
				ImportState: true, ImportStateId: optimizationRuleTestID, ImportStateVerify: true,
			},
			{
				Config: optimizationRuleConfig(server.URL, "enabled = false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "enabled", "false"),
					resource.TestCheckResourceAttr(resourceName, "targets.#", "0"),
					resource.TestCheckResourceAttr(resourceName, "priority", "100"),
					resource.TestCheckNoResourceAttr(resourceName, "description"),
				),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	want := map[string]int{
		"POST " + optimizationRulesURL:                                  1,
		"PUT " + optimizationRulesURL + optimizationRuleTestID + "/":    2,
		"DELETE " + optimizationRulesURL + optimizationRuleTestID + "/": 1,
		"GET " + optimizationRulesURL:                                   1,
	}
	for route, count := range want {
		if api.requests[route] != count {
			t.Errorf("%s requests = %d, want %d", route, api.requests[route], count)
		}
	}
	if len(api.bodies) != 3 {
		t.Fatalf("writes = %d, want 3", len(api.bodies))
	}
	created, cleared := api.bodies[0], api.bodies[2]
	for _, body := range []map[string]any{created, cleared} {
		if targets, _ := body["targets"].([]any); len(targets) != 0 {
			t.Errorf("an omitted targets attribute must send [], got %v", body["targets"])
		}
		if body["description"] != nil || body["trigger_pattern"] != nil {
			t.Errorf("an omitted nullable field must send null, got %v", body)
		}
		if body["priority"] != float64(100) || body["message_role"] != "system" || body["action"] != "transform" {
			t.Errorf("an omitted field must send its default, got %v", body)
		}
	}
	if len(api.rules) != 0 {
		t.Fatal("destroy left the rule in the API")
	}
}

func TestLifecycleGatewayOptimizationRuleBuiltin(t *testing.T) {
	t.Parallel()
	server, api := newOptimizationRuleAPI(t)
	templateSlug := "concise-answers"
	description := "From the catalog."
	api.rules[optimizationBuiltinRuleID] = &logclient.GatewayOptimizationRuleRead{
		ID: optimizationBuiltinRuleID, Name: "Concise answers", Description: &description, Category: "verbosity",
		Instruction: "Answer in at most three sentences.", MessageRole: "system", Priority: 50,
		FailureMode: "fail_open", Action: "transform", Enabled: true, Source: "builtin", TemplateSlug: &templateSlug,
		Targets:   []logclient.GatewayOptimizationTarget{{EndpointSlug: "support"}},
		CreatedAt: "2026-11-20T00:00:00Z", UpdatedAt: "2026-11-20T00:00:00Z",
	}
	const resourceName = "logfire_gateway_optimization_rule.test"
	stored := "description = \"From the catalog.\"\npriority = 50\n"
	config := func(body string) string { return optimizationRuleConfig(server.URL, body) }
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:       config(stored + `targets = [{ endpoint_slug = "support" }]`),
				ResourceName: resourceName, ImportState: true, ImportStateId: "Concise answers", ImportStatePersist: true,
			},
			{
				// The schema default for priority is 100, so an omitted priority changes the stored 50.
				Config:      config(`targets = [{ endpoint_slug = "support" }]`),
				ExpectError: regexp.MustCompile(`(?s)Catalog optimization rule cannot change.*changes:\s+description,\s+priority\.`),
			},
			{
				Config:      config(stored + "message_role = \"user\"\n" + `targets = [{ endpoint_slug = "support" }]`),
				ExpectError: regexp.MustCompile(`(?s)allows\s+only\s+enabled\s+and\s+targets.*changes:\s+message_role\.`),
			},
			{
				Config: config(stored + "enabled = false\n" + `targets = [{ endpoint_slug = "support" }, { endpoint_slug = "docs" }]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", optimizationBuiltinRuleID),
					resource.TestCheckResourceAttr(resourceName, "source", "builtin"),
					resource.TestCheckResourceAttr(resourceName, "template_slug", templateSlug),
					resource.TestCheckResourceAttr(resourceName, "enabled", "false"),
					resource.TestCheckResourceAttr(resourceName, "priority", "50"),
					resource.TestCheckResourceAttr(resourceName, "targets.#", "2"),
				),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.requests["POST "+optimizationRulesURL] != 0 {
		t.Error("an imported builtin rule must not be created again")
	}
	if api.requests["PUT "+optimizationRulesURL+optimizationBuiltinRuleID+"/"] != 1 {
		t.Error("the builtin rule update must send one PUT")
	}
	if len(api.rules) != 0 {
		t.Fatal("destroy left the builtin rule in the API")
	}
}

func TestLifecycleGatewayOptimizationRulePatternCheckUnavailable(t *testing.T) {
	t.Parallel()
	server, api := newOptimizationRuleAPI(t)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      optimizationRuleConfig(server.URL, `trigger_pattern = "uncheckable"`),
			ExpectError: regexp.MustCompile(`(?s)status 503.*could\s+not\s+check\s+a\s+pattern,\s+so\s+Logfire\s+did\s+not\s+save`),
		}},
	})
	api.Lock()
	defer api.Unlock()
	if api.requests["POST "+optimizationRulesURL] != 1 {
		t.Errorf("POST requests = %d, want 1 because a create is not retried", api.requests["POST "+optimizationRulesURL])
	}
}

func TestLifecycleGatewayOptimizationRuleDeletedOutsideTerraform(t *testing.T) {
	t.Parallel()
	server, api := newOptimizationRuleAPI(t)
	const resourceName = "logfire_gateway_optimization_rule.test"
	config := optimizationRuleConfig(server.URL, "")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					delete(api.rules, optimizationRuleTestID)
					api.nextID = optimizationBuiltinRuleID
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionCreate),
				}},
				Check: resource.TestCheckResourceAttr(resourceName, "id", optimizationBuiltinRuleID),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.requests["POST "+optimizationRulesURL] != 2 {
		t.Errorf("POST requests = %d, want 2", api.requests["POST "+optimizationRulesURL])
	}
}

func TestLifecycleGatewayOptimizationRuleCreateConflict(t *testing.T) {
	t.Parallel()
	server, api := newOptimizationRuleAPI(t)
	api.rules[optimizationBuiltinRuleID] = &logclient.GatewayOptimizationRuleRead{ID: optimizationBuiltinRuleID, Name: "Concise answers"}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      optimizationRuleConfig(server.URL, ""),
			ExpectError: regexp.MustCompile(`(?s)already exists.*import the rule by name`),
		}},
	})
}

func TestLifecycleGatewayOptimizationRuleValidation(t *testing.T) {
	t.Parallel()
	config := func(body string) string {
		return lifecycleConfig("http://127.0.0.1:1", "gateway_optimization_rule",
			"name = \"Concise answers\"\ninstruction = \"Be brief.\"\n"+body)
	}
	const verbosity = "category = \"verbosity\"\n"
	slugs := make([]string, 101)
	for i := range slugs {
		slugs[i] = fmt.Sprintf("{ endpoint_slug = \"endpoint-%d\" }", i)
	}
	tooManyTargets := strings.Join(slugs, ", ")
	steps := []resource.TestStep{}
	for body, message := range map[string]string{
		`category = "speed"`:                                                        `value must be one of`,
		verbosity + `message_role = "assistant"`:                                    `value must be one of`,
		verbosity + `failure_mode = "fail_fast"`:                                    `value must be one of`,
		verbosity + `action = "block"`:                                              `value must be one of`,
		verbosity + `priority = 2147483648`:                                         `between -2147483648 and 2147483647`,
		verbosity + `trigger_pattern = ""`:                                          `at least 1`,
		verbosity + `targets = [{ endpoint_slug = "" }]`:                            `at least 1`,
		verbosity + `targets = [{ endpoint_slug = "support", model_pattern = "" }]`: `at least 1`,
		verbosity + "targets = [" + tooManyTargets + "]":                            `at most 100`,
		verbosity + `targets = [{ model_pattern = "gpt-*" }]`:                       `endpoint_slug.*required`,
	} {
		steps = append(steps, resource.TestStep{
			Config: config(body), PlanOnly: true, ExpectError: regexp.MustCompile("(?s)" + message),
		})
	}
	// Logfire limits a name to 2,048 UTF-8 bytes. "é" takes two bytes, so 1,025 of them are 2,050 bytes.
	steps = append(steps, resource.TestStep{
		Config: lifecycleConfig("http://127.0.0.1:1", "gateway_optimization_rule",
			"name = \""+strings.Repeat("é", 1025)+"\"\ninstruction = \"Be brief.\"\n"+verbosity),
		PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)name.*length must be between 1 and 2048`),
	})
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
}
