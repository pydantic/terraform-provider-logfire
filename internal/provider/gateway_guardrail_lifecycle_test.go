// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	prebuiltGuardrailResource = "logfire_gateway_prebuilt_guardrail.test"
	regexGuardrailResource    = "logfire_gateway_regex_guardrail.test"
	presidioGuardrailResource = "logfire_gateway_presidio_guardrail.test"
)

// expectBodyKey checks whether the last write body of a route has a key.
func expectBodyKey(api *guardrailAPI, route func(*terraform.State) string, key string, want bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		r := route(s)
		body := api.lastBody(r)
		if body == nil {
			return fmt.Errorf("no write was sent to %s", r)
		}
		if _, ok := body[key]; ok != want {
			return fmt.Errorf("%s body %v: %q present = %v, want %v", r, body, key, ok, want)
		}
		return nil
	}
}

func fixedRoute(route string) func(*terraform.State) string {
	return func(*terraform.State) string { return route }
}

func itemRoute(method, collection, resourceName string) func(*terraform.State) string {
	return func(s *terraform.State) string {
		return method + " " + collection + s.RootModule().Resources[resourceName].Primary.ID + "/"
	}
}

func TestLifecycleGatewayPrebuiltGuardrail(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	// Someone saved a sample in the UI before Terraform took over the template.
	uiSample := []logclient.GatewayGuardrailSampleMessage{{Role: "user", Content: "Mail me at a@example.com"}}
	api.prebuiltSamples["email-addresses"] = uiSample
	config := func(slug, body string) string {
		return lifecycleConfig(server.URL, "gateway_prebuilt_guardrail", fmt.Sprintf("template_slug = %q\n%s", slug, body))
	}
	const allFlag = `targets = [{ action = "flag" }]`
	const perEndpoint = `targets = [
  { endpoint_slug = "support", action = "redact" },
  { endpoint_slug = "billing", action = "block" },
]`
	const sample = `sample_messages = [{ role = "assistant", content = "Reply to b@example.com" }]`
	putRoute := fixedRoute("PUT " + prebuiltGuardrailsURL + "email-addresses/")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("email-addresses", allFlag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(prebuiltGuardrailResource, "id", "email-addresses"),
					resource.TestCheckResourceAttr(prebuiltGuardrailResource, "category", "pii"),
					resource.TestCheckResourceAttr(prebuiltGuardrailResource, "targets.#", "1"),
					resource.TestCheckNoResourceAttr(prebuiltGuardrailResource, "targets.0.endpoint_slug"),
					resource.TestCheckResourceAttr(prebuiltGuardrailResource, "sample_messages.0.content", uiSample[0].Content),
					expectBodyKey(api, putRoute, "sample_messages", false),
				),
			},
			{
				Config: config("email-addresses", perEndpoint+"\n"+sample),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(prebuiltGuardrailResource, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckTypeSetElemNestedAttrs(prebuiltGuardrailResource, "targets.*", map[string]string{
						"endpoint_slug": "billing", "action": "block",
					}),
					resource.TestCheckResourceAttr(prebuiltGuardrailResource, "sample_messages.0.role", "assistant"),
					expectBodyKey(api, putRoute, "sample_messages", true),
				),
			},
			{
				// Removing sample_messages from the configuration keeps the stored samples.
				Config: config("email-addresses", allFlag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(prebuiltGuardrailResource, "sample_messages.0.role", "assistant"),
					expectBodyKey(api, putRoute, "sample_messages", false),
				),
			},
			{Config: config("email-addresses", allFlag), ResourceName: prebuiltGuardrailResource, ImportState: true, ImportStateId: "email-addresses", ImportStateVerify: true},
			{
				Config: config("credit-cards", allFlag),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(prebuiltGuardrailResource, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr(prebuiltGuardrailResource, "id", "credit-cards"),
			},
			{
				Config:      config("unknown-template", allFlag),
				ExpectError: regexp.MustCompile(`No\s+guardrail\s+template\s+'unknown-template'`),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.prebuilt) != 0 {
		t.Fatalf("destroy left prebuilt guardrails %v", api.prebuilt)
	}
	if api.requests["DELETE "+prebuiltGuardrailsURL+"email-addresses/"] != 1 {
		t.Errorf("replacing the template must delete the old configuration once")
	}
}

func TestLifecycleGatewayRegexGuardrail(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	config := func(body string) string {
		return lifecycleConfig(server.URL, "gateway_regex_guardrail", "name = \"order-ids\"\n"+body)
	}
	const targets = `targets = [{ endpoint_slug = "support", action = "redact" }]`
	created := config("pattern = \"ORD-[0-9]+\"\n" + targets)
	updated := config("description = \"Order numbers\"\npattern = \"ord-[0-9]{6}\"\nflags = \"gi\"\n" + targets +
		"\nsample_messages = [{ role = \"user\", content = \"Where is ORD-123456?\" }]")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: created,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet(regexGuardrailResource, "id"),
					resource.TestCheckResourceAttr(regexGuardrailResource, "flags", ""),
					resource.TestCheckNoResourceAttr(regexGuardrailResource, "description"),
					resource.TestCheckResourceAttr(regexGuardrailResource, "sample_messages.#", "0"),
					resource.TestCheckResourceAttr(regexGuardrailResource, "created_at", guardrailTestCreatedAt),
					expectBodyKey(api, fixedRoute("POST "+regexGuardrailsURL), "sample_messages", false),
					func(*terraform.State) error {
						body := api.lastBody("POST " + regexGuardrailsURL)
						want := []any{map[string]any{
							"target": map[string]any{"type": "endpoint", "endpoint_slug": "support"}, "action": "redact",
						}}
						if !reflect.DeepEqual(body["targets"], want) {
							return fmt.Errorf("targets = %v, want %v", body["targets"], want)
						}
						return nil
					},
				),
			},
			{
				Config: updated,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(regexGuardrailResource, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(regexGuardrailResource, "flags", "gi"),
					resource.TestCheckResourceAttr(regexGuardrailResource, "description", "Order numbers"),
					resource.TestCheckResourceAttr(regexGuardrailResource, "sample_messages.#", "1"),
				),
			},
			{Config: updated, ResourceName: regexGuardrailResource, ImportState: true, ImportStateId: "order-ids", ImportStateVerify: true},
			{
				Config: updated, ResourceName: regexGuardrailResource, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources[regexGuardrailResource].Primary.ID, nil
				},
			},
			{
				// Deleting an endpoint deletes its targets, and the next plan restores them.
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					for _, guardrail := range api.regex {
						guardrail.Targets = []logclient.GatewayGuardrailTarget{}
					}
				},
				Config: updated,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(regexGuardrailResource, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(regexGuardrailResource, "targets.#", "1"),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					clear(api.regex)
				},
				Config:   updated,
				PlanOnly: true, ExpectNonEmptyPlan: true,
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.requests["POST "+regexGuardrailsURL] != 1 {
		t.Errorf("POST requests = %d, want 1", api.requests["POST "+regexGuardrailsURL])
	}
}

func TestLifecycleGatewayPresidioGuardrail(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	config := func(guardrail string) string {
		return fmt.Sprintf(`provider "logfire" {
  base_url = %q
  api_key  = "test-token"
}

resource "logfire_gateway_guardrail_connection" "presidio" {
  name     = "presidio"
  base_url = "https://presidio.example.com"
}

resource "logfire_gateway_presidio_guardrail" "test" {
  name          = "email-addresses"
  connection_id = logfire_gateway_guardrail_connection.presidio.id
  entity_type   = "EMAIL_ADDRESS"
  language      = "en"
  targets       = [{ action = "redact" }]
%s
}`, server.URL, guardrail)
	}
	created := config(`on_error = "block"
operator = { type = "replace", new_value = "<EMAIL>" }
score_threshold = 0.5
sample_texts = ["Mail a@example.com"]`)
	updated := config(`description = "Emails"
on_error = "allow"
operator = { type = "redact" }`)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: created,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrPair(presidioGuardrailResource, "connection_id",
						"logfire_gateway_guardrail_connection.presidio", "id"),
					resource.TestCheckResourceAttr(presidioGuardrailResource, "operator.new_value", "<EMAIL>"),
					resource.TestCheckResourceAttr(presidioGuardrailResource, "score_threshold", "0.5"),
					resource.TestCheckResourceAttr(presidioGuardrailResource, "status", "active"),
					resource.TestCheckNoResourceAttr(presidioGuardrailResource, "inactive_reason"),
					resource.TestCheckResourceAttr(presidioGuardrailResource, "sample_texts.0", "Mail a@example.com"),
				),
			},
			{
				// The guardrail now uses the connection, which a refresh reports.
				Config: created,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: resource.TestCheckResourceAttr("logfire_gateway_guardrail_connection.presidio", "guardrail_count", "1"),
			},
			{
				Config: updated,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(presidioGuardrailResource, "operator.type", "redact"),
					resource.TestCheckNoResourceAttr(presidioGuardrailResource, "operator.new_value"),
					resource.TestCheckNoResourceAttr(presidioGuardrailResource, "score_threshold"),
					resource.TestCheckResourceAttr(presidioGuardrailResource, "sample_texts.0", "Mail a@example.com"),
					expectBodyKey(api, itemRoute("PUT", presidioGuardrailsURL, presidioGuardrailResource), "sample_texts", false),
					func(s *terraform.State) error {
						body := api.lastBody(itemRoute("PUT", presidioGuardrailsURL, presidioGuardrailResource)(s))
						if body["score_threshold"] != nil || !reflect.DeepEqual(body["operator"], map[string]any{"type": "redact"}) {
							return fmt.Errorf("PUT must send the full desired state, got %v", body)
						}
						return nil
					},
				),
			},
			{Config: updated, ResourceName: presidioGuardrailResource, ImportState: true, ImportStateId: "email-addresses", ImportStateVerify: true},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					clear(api.presidio)
				},
				Config:   updated,
				PlanOnly: true, ExpectNonEmptyPlan: true,
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.presidio) != 0 || len(api.connections) != 0 {
		t.Fatalf("destroy left guardrails %v and connections %v", api.presidio, api.connections)
	}
}

func TestLifecycleGatewayGuardrailValidation(t *testing.T) {
	t.Parallel()
	const offline = "http://127.0.0.1:1"
	regex := func(body string) string {
		return lifecycleConfig(offline, "gateway_regex_guardrail", "name = \"ids\"\npattern = \"x\"\n"+body)
	}
	connection := func(body string) string {
		return lifecycleConfig(offline, "gateway_guardrail_connection", "name = \"presidio\"\n"+body)
	}
	presidio := func(body string) string {
		return lifecycleConfig(offline, "gateway_presidio_guardrail", "name = \"emails\"\non_error = \"allow\"\n"+
			"entity_type = \"EMAIL_ADDRESS\"\nlanguage = \"en\"\ntargets = [{ action = \"redact\" }]\n"+body)
	}
	const connectionID = `connection_id = "018f45c0-3cab-7b2f-a8f7-000000000001"`
	const baseURL = `base_url = "https://presidio.example.com"`
	steps := []resource.TestStep{}
	for config, message := range map[string]string{
		regex("targets = []"): "at least 1 elements",
		regex(`targets = [{ endpoint_slug = "a", action = "flag" }, { endpoint_slug = "a", action = "block" }]`): "Endpoint a appears in more than one target",
		regex(`targets = [{ action = "flag" }, { action = "block" }]`):                                           "More than one target omits endpoint_slug",
		regex(`targets = [{ action = "flag" }, { endpoint_slug = "a", action = "block" }]`):                      "which cannot be combined with other targets",
		regex(`targets = [{ endpoint_slug = "a/b", action = "flag" }]`):                                          "must start with a letter or digit",
		regex(`targets = [{ action = "deny" }]`):                                                                 "value must be one of",
		regex("flags = \"x\"\ntargets = [{ action = \"flag\" }]"):                                                "must contain only the flags g, i, m, and s",
		regex("targets = [{ action = \"flag\" }]\nsample_messages = [{ role = \"bot\", content = \"x\" }]"):      "value must be one of",
		connection(baseURL + "\nauth_type = \"bearer\""):                                                         "bearer_token is required when auth_type is bearer",
		connection(baseURL + "\nbearer_token = \"t\""):                                                           "can only be set when auth_type is bearer",
		connection(`base_url = "https://presidio.example.com/"`):                                                 "must be an http or https URL",
		connection(`base_url = "https://user:pass@presidio.example.com"`):                                        "must be an http or https URL",
		connection(baseURL + "\ntimeout_ms = 50"):                                                                "between 100 and 10000",
		presidio(connectionID + "\noperator = { type = \"replace\" }"):                                           "new_value is required when the operator type is replace",
		presidio(connectionID + "\noperator = { type = \"redact\", new_value = \"x\" }"):                         "new_value can only be set when the operator type is replace",
		presidio(connectionID + "\nscore_threshold = 2"):                                                         "between 0.000000 and 1.000000",
		presidio(`connection_id = "018F45C0-3CAB-7B2F-A8F7-000000000001"`):                                       "must be a lowercase connection UUID",
		lifecycleConfig(offline, "gateway_prebuilt_guardrail", "template_slug = \"pii\"\ntargets = []"):          "at least 1 elements",
		lifecycleConfig(offline, "gateway_prebuilt_guardrail", "template_slug = \"pii\"\ntargets = [{ action = \"flag\" }]\n"+
			"sample_messages = [{ role = \"user\", content = \""+strings.Repeat("x", 4001)+"\" }]"): "length must be at most 4000",
		regex("targets = [{ action = \"flag\" }]\nsample_messages = [{ role = \"user\", content = \"" + strings.Repeat("x", 20001) + "\" }]"): "length must be at most 20000",
		lifecycleConfig(offline, "gateway_guardrail_connection", baseURL+"\nname = \""+strings.Repeat("n", 256)+"\""):                         "length must be between 1 and 255",
	} {
		pattern := regexp.MustCompile(strings.ReplaceAll(regexp.QuoteMeta(message), " ", `\s+`))
		steps = append(steps, resource.TestStep{Config: config, PlanOnly: true, ExpectError: pattern})
	}
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
}

func TestGatewayGuardrailValidationDefersUnknownTargets(t *testing.T) {
	t.Parallel()
	for name, r := range map[string]interface {
		fwresource.Resource
		fwresource.ResourceWithValidateConfig
	}{
		"prebuilt": &GatewayPrebuiltGuardrailResource{},
		"regex":    &GatewayRegexGuardrailResource{},
		"presidio": &GatewayPresidioGuardrailResource{},
	} {
		t.Run(name, func(t *testing.T) {
			var schemaResponse fwresource.SchemaResponse
			r.Schema(t.Context(), fwresource.SchemaRequest{}, &schemaResponse)
			targetsType, ok := schemaResponse.Schema.Attributes["targets"].GetType().TerraformType(t.Context()).(tftypes.Set)
			if !ok {
				t.Fatal("targets must be a set")
			}
			targetType, ok := targetsType.ElementType.(tftypes.Object)
			if !ok {
				t.Fatal("a target must be an object")
			}
			attributeTypes := map[string]tftypes.Type{}
			attributes := map[string]tftypes.Value{}
			for attribute, schemaAttribute := range schemaResponse.Schema.Attributes {
				attributeTypes[attribute] = schemaAttribute.GetType().TerraformType(t.Context())
				attributes[attribute] = tftypes.NewValue(attributeTypes[attribute], nil)
			}
			// An unknown slug next to an all-endpoints target can still turn out valid, so validation waits.
			attributes["targets"] = tftypes.NewValue(targetsType, []tftypes.Value{
				tftypes.NewValue(targetType, map[string]tftypes.Value{
					"endpoint_slug": tftypes.NewValue(tftypes.String, nil),
					"action":        tftypes.NewValue(tftypes.String, "flag"),
				}),
				tftypes.NewValue(targetType, tftypes.UnknownValue),
			})
			config := tfsdk.Config{
				Schema: schemaResponse.Schema,
				Raw:    tftypes.NewValue(tftypes.Object{AttributeTypes: attributeTypes}, attributes),
			}
			var response fwresource.ValidateConfigResponse
			r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: config}, &response)
			if response.Diagnostics.HasError() {
				t.Fatalf("an unknown target must defer validation: %v", response.Diagnostics)
			}
		})
	}
}

func TestLifecycleGatewayGuardrailPlanAnswers(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	regexConfig := func(action string) string {
		return lifecycleConfig(server.URL, "gateway_regex_guardrail",
			"name = \"order-ids\"\npattern = \"ORD-[0-9]+\"\ntargets = [{ action = \""+action+"\" }]")
	}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.enforcementDenied = true
				},
				Config:      regexConfig("block"),
				ExpectError: regexp.MustCompile(`needs\s+the\s+gateway_guardrail_enforcement\s+feature`),
			},
			{
				// An unknown endpoint slug fails, and Logfire names it.
				Config:      lifecycleConfig(server.URL, "gateway_regex_guardrail", "name = \"order-ids\"\npattern = \"ORD-[0-9]+\"\ntargets = [{ endpoint_slug = \""+unknownEndpointSlug+"\", action = \"observe\" }]"),
				ExpectError: regexp.MustCompile(`Not\s+found:\s+'unknown-endpoint'`),
			},
			{Config: regexConfig("observe")},
			{
				// A plan downgrade keeps the guardrail in state and reports the plan answer.
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.planDenied = true
				},
				Config:      regexConfig("observe"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Gateway\s+guardrail\s+management\s+through\s+the\s+API\s+is\s+available\s+only`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.planDenied = false
				},
				Config: regexConfig("observe"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
		},
	})
}

func TestLifecycleGatewayPresidioPlanAnswers(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	api.presidioDenied = true
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      guardrailConnectionConfig(server.URL, "name = \"presidio\"\nbase_url = \"https://presidio.example.com\""),
			ExpectError: regexp.MustCompile(`Presidio\s+PII\s+detection\s+guardrails\s+require\s+the\s+Enterprise\s+Cloud\s+plan`),
		}},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.connections) != 0 {
		t.Fatalf("a refused write must store nothing: %v", api.connections)
	}
}

func TestLifecycleGatewayRegexGuardrailPatternCheckUnavailable(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	api.patternCheckDown = true
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: lifecycleConfig(server.URL, "gateway_regex_guardrail",
				"name = \"order-ids\"\npattern = \"ORD-[0-9]+\"\ntargets = [{ action = \"observe\" }]"),
			ExpectError: regexp.MustCompile(`Logfire\s+stored\s+nothing`),
		}},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.regex) != 0 || api.requests["POST "+regexGuardrailsURL] != 1 {
		t.Fatalf("a pattern check failure must send one create and store nothing: %v", api.requests)
	}
}
