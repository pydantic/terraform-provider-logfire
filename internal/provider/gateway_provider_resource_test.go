// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/pydantic/terraform-provider-logfire/internal/client"
	"github.com/pydantic/terraform-provider-logfire/internal/provider"
)

type gatewayFailingTransport struct {
	check func(*http.Request)
	err   error
}

func (transport gatewayFailingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.check(req)
	return nil, transport.err
}

func gatewayResource(t *testing.T, transport http.RoundTripper) (*provider.GatewayProviderResource, schema.Schema) {
	t.Helper()
	r := &provider.GatewayProviderResource{}
	var response resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &response)
	if transport != nil {
		c, err := client.NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		var configured resource.ConfigureResponse
		r.Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &configured)
		if configured.Diagnostics.HasError() {
			t.Fatal(configured.Diagnostics)
		}
	}
	return r, response.Schema
}

func gatewayValue(t *testing.T, s schema.Schema, values map[string]any) tftypes.Value {
	t.Helper()
	attributeTypes := map[string]tftypes.Type{}
	attributes := map[string]tftypes.Value{}
	for name, attribute := range s.Attributes {
		attributeTypes[name] = attribute.GetType().TerraformType(t.Context())
		attributes[name] = tftypes.NewValue(attributeTypes[name], values[name])
	}
	return tftypes.NewValue(tftypes.Object{AttributeTypes: attributeTypes}, attributes)
}

func TestGatewayProviderConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name      string
		vendor    any
		api       any
		region    any
		endpoint  any
		wantError bool
	}{
		{"fixed vendor", "openai", nil, nil, nil, false},
		{"bedrock", "bedrock", "runtime", "us-east-1", nil, false},
		{"missing API", "bedrock", nil, "us-east-1", nil, true},
		{"missing region", "bedrock", "runtime", nil, nil, true},
		{"unexpected API", "openai", "runtime", nil, nil, true},
		{"unexpected region", "openai", nil, "us-east-1", nil, true},
		{"unknown vendor", tftypes.UnknownValue, nil, nil, nil, false},
		{"unknown region", "bedrock", "runtime", tftypes.UnknownValue, nil, false},
		{"azure", "azure", nil, nil, "https://my-resource.openai.azure.com", false},
		{"google-vertex", "google-vertex", nil, nil, "https://aiplatform.googleapis.com", false},
		{"missing endpoint", "google-vertex", nil, nil, nil, true},
		{"unexpected endpoint", "bedrock", "runtime", "us-east-1", "https://aiplatform.googleapis.com", true},
		{"unknown endpoint", "azure", nil, nil, tftypes.UnknownValue, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, s := gatewayResource(t, nil)
			config := tfsdk.Config{Schema: s, Raw: gatewayValue(t, s, map[string]any{
				"slug": "primary", "vendor": tt.vendor, "api_key": "synthetic-key",
				"bedrock_api": tt.api, "bedrock_region": tt.region, "endpoint": tt.endpoint,
			})}
			var response resource.ValidateConfigResponse
			r.ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: config}, &response)
			if response.Diagnostics.HasError() != tt.wantError {
				t.Fatalf("validation diagnostics = %v", response.Diagnostics)
			}
		})
	}
}

func TestGatewayProviderMutationRequests(t *testing.T) {
	for _, tt := range []struct {
		name     string
		create   bool
		vendor   string
		key      string
		region   string
		endpoint string
		want     string
	}{
		{"create OpenAI", true, "openai", "synthetic-key", "", "", `{
			"slug":"primary", "provider":{"vendor":"openai",
			"config":{"credentials":{"type":"api-key","value":"synthetic-key"}}},
			"pricing":{"required":false}}`},
		{"create Bedrock", true, "bedrock", "synthetic-key", "us-east-1", "", `{
			"slug":"primary", "provider":{"vendor":"bedrock",
			"config":{"credentials":{"type":"api-key","value":"synthetic-key"},
			"api":"runtime","region":"us-east-1"}}, "pricing":{"required":false}}`},
		{"create Azure", true, "azure", "synthetic-key", "", "", `{
			"slug":"primary", "provider":{"vendor":"azure",
			"config":{"credentials":{"type":"api-key","value":"synthetic-key"},
			"endpoint":"https://primary.openai.azure.com"}}, "pricing":{"required":false}}`},
		{"create Vertex", true, "google-vertex", "{}", "", "", `{
			"slug":"primary", "provider":{"vendor":"google-vertex",
			"config":{"credentials":{"type":"service-account-json","value":"{}"},
			"endpoint":"https://aiplatform.googleapis.com"}}, "pricing":{"required":false}}`},
		{"pricing preserves credentials", false, "openai", "synthetic-key", "", "", `{"pricing":{"required":false}}`},
		{"Vertex pricing preserves credentials", false, "google-vertex", "synthetic-key", "", "",
			`{"pricing":{"required":false}}`},
		{"credential rotation", false, "openai", "rotated-key", "", "", `{
			"provider":{"vendor":"openai","config":{"credentials":{"type":"api-key","value":"rotated-key"}}},
			"pricing":{"required":false}}`},
		{"Vertex credential rotation", false, "google-vertex", "rotated-key", "", "", `{
			"provider":{"vendor":"google-vertex",
			"config":{"credentials":{"type":"service-account-json","value":"rotated-key"}}},
			"pricing":{"required":false}}`},
		{"endpoint change resends credentials", false, "google-vertex", "synthetic-key", "",
			"https://europe-west4-aiplatform.googleapis.com", `{
			"provider":{"vendor":"google-vertex",
			"config":{"credentials":{"type":"service-account-json","value":"synthetic-key"},
			"endpoint":"https://europe-west4-aiplatform.googleapis.com"}}, "pricing":{"required":false}}`},
		{"Bedrock region preserves credentials", false, "bedrock", "synthetic-key", "eu-west-1", "", `{
			"provider":{"vendor":"bedrock","config":{"region":"eu-west-1"}}, "pricing":{"required":false}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			r, s := gatewayResource(t, gatewayFailingTransport{
				err: errors.New("injected transport failure"),
				check: func(request *http.Request) {
					calls++
					method, requestPath := http.MethodPatch, "/api/v1/gateway/providers/provider-id/"
					if tt.create {
						method, requestPath = http.MethodPost, "/api/v1/gateway/providers/"
					}
					if request.Method != method || request.URL.Path != requestPath {
						t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
					}
					var actual, want any
					if err := json.NewDecoder(request.Body).Decode(&actual); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(actual, want) {
						t.Fatalf("request = %v, want %v", actual, want)
					}
				},
			})
			values := map[string]any{
				"id": "provider-id", "slug": "primary", "vendor": tt.vendor,
				"api_key": "synthetic-key", "require_pricing": true,
			}
			switch tt.vendor {
			case "bedrock":
				values["bedrock_api"], values["bedrock_region"] = "runtime", "us-east-1"
			case "azure":
				values["endpoint"] = "https://primary.openai.azure.com"
			case "google-vertex":
				values["endpoint"] = "https://aiplatform.googleapis.com"
			}
			state := tfsdk.State{Schema: s, Raw: gatewayValue(t, s, values)}
			values["api_key"], values["require_pricing"] = tt.key, false
			if tt.region != "" {
				values["bedrock_region"] = tt.region
			}
			if tt.endpoint != "" {
				values["endpoint"] = tt.endpoint
			}
			plan := tfsdk.Plan{Schema: s, Raw: gatewayValue(t, s, values)}
			if tt.create {
				var response resource.CreateResponse
				r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &response)
				if !response.Diagnostics.HasError() {
					t.Fatal("create must report the transport failure")
				}
			} else {
				response := resource.UpdateResponse{State: state}
				r.Update(t.Context(), resource.UpdateRequest{Plan: plan, State: state}, &response)
				if !response.Diagnostics.HasError() || !response.State.Raw.Equal(state.Raw) {
					t.Fatal("failed update must preserve state and report an error")
				}
			}
			if calls != 1 {
				t.Fatalf("transport calls = %d, want 1", calls)
			}
		})
	}
}

func TestGatewayProviderReadFailures(t *testing.T) {
	for _, status := range []int{
		http.StatusNotFound, http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r, s := gatewayResource(t, gatewayFailingTransport{
				err: &client.APIError{StatusCode: status, Message: "synthetic-secret"},
				check: func(req *http.Request) {
					if req.Method != http.MethodGet || req.URL.Path != "/api/v1/gateway/providers/provider-id/" {
						t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
					}
				},
			})
			state := tfsdk.State{Schema: s, Raw: gatewayValue(t, s, map[string]any{"id": "provider-id"})}
			response := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
			if status == http.StatusNotFound {
				if !response.State.Raw.IsNull() || response.Diagnostics.HasError() {
					t.Fatal("missing provider must be removed from state without an error")
				}
			} else if !response.State.Raw.Equal(state.Raw) || !response.Diagnostics.HasError() {
				t.Fatal("inaccessible provider must remain in state with an error")
			}
			for _, diagnostic := range response.Diagnostics {
				if strings.Contains(diagnostic.Detail(), "synthetic-secret") {
					t.Fatal("diagnostic exposed the upstream error body")
				}
			}
		})
	}
}

func TestGatewayProviderDeleteFailure(t *testing.T) {
	r, s := gatewayResource(t, gatewayFailingTransport{
		err: &client.APIError{StatusCode: http.StatusServiceUnavailable, Message: "synthetic-secret"},
		check: func(req *http.Request) {
			if req.Method != http.MethodDelete || req.URL.Path != "/api/v1/gateway/providers/provider-id/" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
		},
	})
	state := tfsdk.State{Schema: s, Raw: gatewayValue(t, s, map[string]any{"id": "provider-id"})}
	response := resource.DeleteResponse{State: state}
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() || !response.State.Raw.Equal(state.Raw) {
		t.Fatal("failed delete must preserve state and report an error")
	}
	for _, diagnostic := range response.Diagnostics {
		if strings.Contains(diagnostic.Detail(), "synthetic-secret") {
			t.Fatal("diagnostic exposed the upstream error body")
		}
	}
}

func TestGatewayProviderImportByUUID(t *testing.T) {
	r, s := gatewayResource(t, nil)
	response := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: gatewayValue(t, s, nil)}}
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed11"}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	want := gatewayValue(t, s, map[string]any{"id": "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed11"})
	if !response.State.Raw.Equal(want) {
		t.Fatalf("imported state = %v", response.State.Raw)
	}
}

type gatewayListTransport struct {
	body string
	path string
}

func (t gatewayListTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.path == "" {
		t.path = "/api/v1/gateway/providers/"
	}
	if req.Method != http.MethodGet || req.URL.Path != t.path {
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

func TestGatewayProviderImportBySlug(t *testing.T) {
	list := `{"providers":[
		{"id":"11111111-1111-1111-1111-111111111111","slug":"anthropic","provider":{"vendor":"anthropic"},"pricing":{"required":true},"created_at":"2026-01-01T00:00:00Z"},
		{"id":"018f45c0-3cab-7b2f-a8f7-8a0b55a7ed11","slug":"openai","provider":{"vendor":"openai"},"pricing":{"required":true},"created_at":"2026-01-01T00:00:00Z"}
	],"next_cursor":null}`
	r, s := gatewayResource(t, gatewayListTransport{body: list})
	response := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: gatewayValue(t, s, nil)}}
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: " openai "}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	want := gatewayValue(t, s, map[string]any{"id": "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed11"})
	if !response.State.Raw.Equal(want) {
		t.Fatalf("imported state = %v", response.State.Raw)
	}
}

func TestGatewayProviderImportBySlugNotFound(t *testing.T) {
	list := `{"providers":[],"next_cursor":null}`
	r, s := gatewayResource(t, gatewayListTransport{body: list})
	response := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: gatewayValue(t, s, nil)}}
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: "missing-slug"}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("expected an import error for an unknown slug")
	}
}

func TestGatewayProviderReadEndpoint(t *testing.T) {
	for _, vendor := range []string{"azure", "google-vertex"} {
		t.Run(vendor, func(t *testing.T) {
			body := `{"id":"provider-id","slug":"primary","provider":{"vendor":"` + vendor +
				`","config":{"endpoint":"https://aiplatform.googleapis.com"}},` +
				`"pricing":{"required":true},"created_at":"2026-01-01T00:00:00Z"}`
			r, s := gatewayResource(t, gatewayListTransport{body: body, path: "/api/v1/gateway/providers/provider-id/"})
			values := map[string]any{"id": "provider-id", "api_key": "synthetic-key"}
			state := tfsdk.State{Schema: s, Raw: gatewayValue(t, s, values)}
			response := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			var endpoint, apiKey types.String
			response.State.GetAttribute(t.Context(), path.Root("endpoint"), &endpoint)
			response.State.GetAttribute(t.Context(), path.Root("api_key"), &apiKey)
			if endpoint.ValueString() != "https://aiplatform.googleapis.com" || apiKey.ValueString() != "synthetic-key" {
				t.Fatalf("endpoint = %v, api_key = %v", endpoint, apiKey)
			}
		})
	}
}
