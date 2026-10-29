// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	endpointTestID  = "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed00"
	endpointOpenAI  = "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed11"
	endpointClaude  = "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed22"
	endpointItemURL = "/api/v1/gateway/endpoints/" + endpointTestID + "/"
)

type endpointAPI struct {
	sync.Mutex
	endpoint *logclient.GatewayEndpointRead
	requests map[string]int
}

// newEndpointAPI mimics the public endpoint API, including float32 weight storage and priority ordering.
func newEndpointAPI(t *testing.T) (*httptest.Server, *endpointAPI) {
	t.Helper()
	api := &endpointAPI{requests: map[string]int{}}
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
		var payload logclient.GatewayEndpointCreate
		if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("%s body: %v", route, err)
			}
		}
		for i := range payload.Providers {
			if payload.Providers[i].Weight == 0 {
				t.Errorf("%s sent provider %s without a weight", route, payload.Providers[i].ProviderID)
			}
			payload.Providers[i].Weight = float64(float32(payload.Providers[i].Weight))
		}
		sort.SliceStable(payload.Providers, func(i, j int) bool {
			return payload.Providers[i].Priority > payload.Providers[j].Priority
		})
		switch route {
		case "POST /api/v1/gateway/endpoints/":
			api.endpoint = &logclient.GatewayEndpointRead{
				ID: endpointTestID, Slug: payload.Slug, Description: payload.Description,
				Providers: payload.Providers, CreatedAt: "2026-10-29T00:00:00Z",
			}
			w.WriteHeader(http.StatusCreated)
		case "GET /api/v1/gateway/endpoints/":
			page := logclient.GatewayEndpointsPage{Endpoints: []logclient.GatewayEndpointRead{}}
			if api.endpoint != nil && api.endpoint.Slug == r.URL.Query().Get("slug") {
				page.Endpoints = append(page.Endpoints, *api.endpoint)
			}
			_ = json.NewEncoder(w).Encode(page)
			return
		case "PATCH " + endpointItemURL:
			api.endpoint.Description = payload.Description
		case "PUT " + endpointItemURL + "providers/":
			api.endpoint.Providers = payload.Providers
		case "DELETE " + endpointItemURL:
			api.endpoint = nil
			w.WriteHeader(http.StatusNoContent)
			return
		case "GET " + endpointItemURL:
			if api.endpoint == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"Endpoint not found in this organization."}`))
				return
			}
		default:
			t.Errorf("unexpected API request: %s", route)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(api.endpoint)
	}))
	t.Cleanup(server.Close)
	return server, api
}

func TestLifecycleGatewayEndpoint(t *testing.T) {
	t.Parallel()
	server, api := newEndpointAPI(t)
	config := func(description, providers string) string {
		return lifecycleConfig(server.URL, "gateway_endpoint", fmt.Sprintf("slug = \"default\"\n%s\nproviders = [%s]", description, providers))
	}
	openAI := fmt.Sprintf("{ provider_id = %q, weight = 0.1 }", endpointOpenAI)
	claude := fmt.Sprintf("{ provider_id = %q, priority = 10, active = false }", endpointClaude)
	const resourceName = "logfire_gateway_endpoint.test"
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("", openAI),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", endpointTestID),
					resource.TestCheckNoResourceAttr(resourceName, "description"),
					resource.TestCheckTypeSetElemNestedAttrs(resourceName, "providers.*", map[string]string{
						"provider_id": endpointOpenAI, "weight": "0.1",
					}),
				),
			},
			{
				Config: config(`description = "Primary"`, openAI+", "+claude),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "description", "Primary"),
					resource.TestCheckResourceAttr(resourceName, "providers.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs(resourceName, "providers.*", map[string]string{
						"provider_id": endpointClaude, "priority": "10", "active": "false",
					}),
				),
			},
			{Config: config(`description = "Primary"`, openAI+", "+claude), ResourceName: resourceName, ImportState: true, ImportStateId: "default", ImportStateVerify: true},
			{Config: config("", openAI+", "+claude), Check: resource.TestCheckNoResourceAttr(resourceName, "description")},
		},
	})
	api.Lock()
	defer api.Unlock()
	want := map[string]int{
		"POST /api/v1/gateway/endpoints/":       1,
		"PATCH " + endpointItemURL:              2,
		"PUT " + endpointItemURL + "providers/": 1,
		"DELETE " + endpointItemURL:             1,
		"GET /api/v1/gateway/endpoints/":        1,
	}
	for route, count := range want {
		if api.requests[route] != count {
			t.Errorf("%s requests = %d, want %d", route, api.requests[route], count)
		}
	}
	if api.endpoint != nil {
		t.Fatal("destroy left the endpoint in the API")
	}
}

func TestLifecycleGatewayEndpointValidation(t *testing.T) {
	t.Parallel()
	config := func(body string) string {
		return lifecycleConfig("http://127.0.0.1:1", "gateway_endpoint", "slug = \"default\"\n"+body)
	}
	providers := func(elements string) string { return "providers = [" + elements + "]" }
	openAI := fmt.Sprintf("provider_id = %q", endpointOpenAI)
	steps := []resource.TestStep{}
	for body, message := range map[string]string{
		providers(""): "at least 1 elements",
		providers("{ " + openAI + ", active = false }"):                                 "At least one provider must be active",
		providers("{ " + openAI + ", weight = 0 }"):                                     "positive finite number",
		providers("{ " + openAI + ", weight = 1e40 }"):                                  "positive finite number",
		providers("{ provider_id = \"openai\" }"):                                       "must be a lowercase provider UUID",
		providers(fmt.Sprintf("{ provider_id = %q }", strings.ToUpper(endpointOpenAI))): "must be a lowercase provider UUID",
		providers("{ " + openAI + ", priority = -1 }"):                                  "between 0 and 2147483647",
		providers("{ " + openAI + ", priority = 1 }, { " + openAI + " }"):               "Duplicate provider",
		"description = \"Primary\\n\"\n" + providers("{ "+openAI+" }"):                  "must not start or end with whitespace",
	} {
		steps = append(steps, resource.TestStep{Config: config(body), PlanOnly: true, ExpectError: regexp.MustCompile(message)})
	}
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
}

func TestGatewayEndpointValidationDefersUnknownProvider(t *testing.T) {
	t.Parallel()
	r := &GatewayEndpointResource{}
	var schemaResponse fwresource.SchemaResponse
	r.Schema(t.Context(), fwresource.SchemaRequest{}, &schemaResponse)
	providersType, ok := schemaResponse.Schema.Attributes["providers"].GetType().TerraformType(t.Context()).(tftypes.Set)
	if !ok {
		t.Fatal("providers must be a set")
	}
	attributeTypes := map[string]tftypes.Type{}
	attributes := map[string]tftypes.Value{}
	for name, attribute := range schemaResponse.Schema.Attributes {
		attributeTypes[name] = attribute.GetType().TerraformType(t.Context())
		attributes[name] = tftypes.NewValue(attributeTypes[name], nil)
	}
	attributes["slug"] = tftypes.NewValue(tftypes.String, "default")
	attributes["providers"] = tftypes.NewValue(attributeTypes["providers"], []tftypes.Value{
		tftypes.NewValue(providersType.ElementType, tftypes.UnknownValue),
	})
	config := tfsdk.Config{
		Schema: schemaResponse.Schema,
		Raw:    tftypes.NewValue(tftypes.Object{AttributeTypes: attributeTypes}, attributes),
	}
	var response fwresource.ValidateConfigResponse
	r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: config}, &response)
	if response.Diagnostics.HasError() {
		t.Fatalf("unknown provider must defer validation: %v", response.Diagnostics)
	}
}
