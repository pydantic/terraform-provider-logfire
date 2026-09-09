// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type apiKeyStubTransport struct {
	check  func(*http.Request)
	status int
	body   string
}

func (s apiKeyStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.check(req)
	return &http.Response{
		StatusCode: s.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Request:    req,
	}, nil
}

func newAPIKeyTestClient(t *testing.T, transport http.RoundTripper) *APIClient {
	t.Helper()
	c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decodeBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

const apiKeyListResponse = `[
  {"id":"11111111-1111-1111-1111-111111111111","organization_id":"22222222-2222-2222-2222-222222222222",
   "name":"otel","description":null,"scopes":["project:read_otlp","project:write_otlp"],
   "project_id":"33333333-3333-3333-3333-333333333333","project_name":"prod","all_projects":false,
   "created_by":null,"created_by_name":"Ada","created_at":"2026-01-01T00:00:00Z","last_used_at":null,
   "expires_at":null,"user_id":null,"active":true,"updated_at":null,"updated_by":null,"claims":{}},
  {"id":"44444444-4444-4444-4444-444444444444","organization_id":"22222222-2222-2222-2222-222222222222",
   "name":"gateway","description":"llm","scopes":["project:gateway_proxy"],
   "project_id":"33333333-3333-3333-3333-333333333333","project_name":"prod","all_projects":false,
   "created_by":null,"created_by_name":null,"created_at":"2026-01-02T00:00:00Z","last_used_at":null,
   "expires_at":null,"user_id":null,"active":true,"updated_at":null,"updated_by":null,
   "claims":{"project:gateway_proxy":{"spending_limit_daily":10,"spending_limit_weekly":null,
   "spending_limit_monthly":null,"spending_limit_total":null,"cache_enabled":true}}}
]`

func TestCreateAPIKeyRequest(t *testing.T) {
	t.Parallel()
	daily := 10
	cache := true
	c := newAPIKeyTestClient(t, apiKeyStubTransport{
		status: http.StatusCreated,
		body: `{"api_key": {"id":"11111111-1111-1111-1111-111111111111","organization_id":"22222222-2222-2222-2222-222222222222",
			"name":"otel","description":null,"scopes":["project:write_otlp"],"project_id":"33333333-3333-3333-3333-333333333333",
			"project_name":"prod","all_projects":false,"created_by":null,"created_by_name":null,
			"created_at":"2026-01-01T00:00:00Z","last_used_at":null,"expires_at":null,"user_id":null,
			"active":true,"updated_at":null,"updated_by":null,"claims":{}}, "token":"secret"}`,
		check: func(req *http.Request) {
			if req.Method != http.MethodPost || req.URL.Path != "/api/v1/api-keys/" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
			body := decodeBody(t, req)
			if body["name"] != "otel" {
				t.Fatalf("name = %v", body["name"])
			}
			if scopes, ok := body["scopes"].([]any); !ok || len(scopes) != 1 || scopes[0] != "project:write_otlp" {
				t.Fatalf("scopes = %v", body["scopes"])
			}
			if body["project_id"] != "33333333-3333-3333-3333-333333333333" {
				t.Fatalf("project_id = %v", body["project_id"])
			}
			claims, ok := body["claims"].(map[string]any)
			if !ok {
				t.Fatalf("claims = %v", body["claims"])
			}
			proxy, ok := claims["project:gateway_proxy"].(map[string]any)
			if !ok || proxy["spending_limit_daily"] != float64(daily) || proxy["cache_enabled"] != cache {
				t.Fatalf("gateway claims = %v", claims)
			}
		},
	})
	expires := "2027-01-01T00:00:00Z"
	description := "ingest"
	out, err := c.CreateAPIKey(context.Background(), APIKeyCreate{
		Name:        "otel",
		Scopes:      []string{"project:write_otlp"},
		Description: &description,
		Claims:      &ScopeClaims{GatewayProxy: &GatewayProxyClaims{SpendingLimitDaily: &daily, CacheEnabled: &cache}},
		ProjectID:   strPtr("33333333-3333-3333-3333-333333333333"),
		ExpiresAt:   &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Token != "secret" || out.APIKey.ID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestCreateAPIKeyOmitsEmptyOptionals(t *testing.T) {
	t.Parallel()
	c := newAPIKeyTestClient(t, apiKeyStubTransport{
		status: http.StatusCreated,
		body:   `{"api_key": {"id":"id","organization_id":"org","name":"mgmt","scopes":["organization:read"],"all_projects":true,"active":true,"created_at":"2026-01-01T00:00:00Z","claims":{}}, "token":"secret"}`,
		check: func(req *http.Request) {
			body := decodeBody(t, req)
			for _, key := range []string{"description", "claims", "project_id", "expires_at"} {
				if _, present := body[key]; present {
					t.Fatalf("expected %q to be omitted, got %v", key, body[key])
				}
			}
		},
	})
	if _, err := c.CreateAPIKey(context.Background(), APIKeyCreate{Name: "mgmt", Scopes: []string{"organization:read"}}); err != nil {
		t.Fatal(err)
	}
}

func TestGetAPIKeyFromList(t *testing.T) {
	t.Parallel()
	c := newAPIKeyTestClient(t, apiKeyStubTransport{
		status: http.StatusOK,
		body:   apiKeyListResponse,
		check: func(req *http.Request) {
			if req.Method != http.MethodGet || req.URL.Path != "/api/v1/api-keys/" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
		},
	})
	found, err := c.GetAPIKey(context.Background(), "44444444-4444-4444-4444-444444444444")
	if err != nil {
		t.Fatal(err)
	}
	if found.Name != "gateway" || found.Claims.GatewayProxy == nil || *found.Claims.GatewayProxy.SpendingLimitDaily != 10 {
		t.Fatalf("unexpected key: %+v", found)
	}
	if _, err := c.GetAPIKey(context.Background(), "missing"); !IsNotFoundError(err) {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestUpdateAPIKeyRequest(t *testing.T) {
	t.Parallel()
	c := newAPIKeyTestClient(t, apiKeyStubTransport{
		status: http.StatusOK,
		body: `{"id":"11111111-1111-1111-1111-111111111111","organization_id":"org","name":"renamed",
			"scopes":["project:write_otlp"],"all_projects":false,"active":true,"created_at":"2026-01-01T00:00:00Z","claims":{}}`,
		check: func(req *http.Request) {
			if req.Method != http.MethodPatch || req.URL.Path != "/api/v1/api-keys/11111111-1111-1111-1111-111111111111/" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
			body := decodeBody(t, req)
			if body["name"] != "renamed" {
				t.Fatalf("name = %v", body["name"])
			}
			if desc, present := body["description"]; !present || desc != nil {
				t.Fatalf("description should be explicit null, got %v (present=%v)", desc, present)
			}
			claims, ok := body["claims"].(map[string]any)
			if !ok {
				t.Fatalf("claims = %v", body["claims"])
			}
			proxy, ok := claims["project:gateway_proxy"].(map[string]any)
			if !ok || proxy["spending_limit_daily"] != nil || proxy["cache_enabled"] != false {
				t.Fatalf("gateway claims = %v", claims)
			}
		},
	})
	name := "renamed"
	claims := map[string]any{"project:gateway_proxy": map[string]any{"spending_limit_daily": nil, "cache_enabled": false}}
	out, err := c.UpdateAPIKey(context.Background(), "11111111-1111-1111-1111-111111111111", APIKeyUpdate{
		Name:        NullableFieldValue(name),
		Description: NullableFieldNull[string](),
		Claims:      &claims,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "renamed" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestDeleteAPIKeyRequest(t *testing.T) {
	t.Parallel()
	c := newAPIKeyTestClient(t, apiKeyStubTransport{
		status: http.StatusNoContent,
		body:   ``,
		check: func(req *http.Request) {
			if req.Method != http.MethodDelete || req.URL.Path != "/api/v1/api-keys/11111111-1111-1111-1111-111111111111/" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
		},
	})
	if err := c.DeleteAPIKey(context.Background(), "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatal(err)
	}
}

func strPtr(s string) *string { return &s }
