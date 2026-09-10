// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// roundTripFunc adapts a function into an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func formBody(req *http.Request) url.Values {
	data, _ := io.ReadAll(req.Body)
	values, _ := url.ParseQuery(string(data))
	return values
}

const organizationReadBody = `{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","organization_name":"acme",
	"subscription_plan":null,"has_admin_panel":true,"created_at":"2026-01-01T00:00:00Z",
	"updated_at":"2026-01-01T00:00:00Z","billing_email":null,"organization_display_name":null,
	"github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
	"spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
	"gateway_enabled":false,"ai_enabled":false}`

const exchangeOKBody = `{"access_token":"exchanged-token","token_type":"Bearer","expires_in":900,
	"scope":"organization:read organization:write","issued_token_type":"urn:ietf:params:oauth:token-type:access_token"}`

// exchangeTransport answers the OAuth token exchange with a valid token.
func exchangeTransport() roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/oauth/token" {
			return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		if ct := req.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			return nil, fmt.Errorf("exchange content type = %s", ct)
		}
		if auth := req.Header.Get("Authorization"); auth != "" {
			return nil, fmt.Errorf("exchange must not send an Authorization header, got %q", auth)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(exchangeOKBody)),
			Request:    req,
		}, nil
	}
}

// TestOrganizationCreateListRoutes pins the create/list migration to the
// instance routes.
func TestOrganizationCreateListRoutes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		call   func(*APIClient) error
		method string
		path   string
	}{
		{
			name:   "create uses instance route",
			status: http.StatusCreated,
			call: func(c *APIClient) error {
				_, err := c.CreateOrganization(context.Background(), OrganizationCreate{OrganizationName: "acme"})
				return err
			},
			method: http.MethodPost, path: "/api/v1/instance/organizations/",
		},
		{
			name:   "list uses instance route",
			status: http.StatusOK,
			call: func(c *APIClient) error {
				_, err := c.ListOrganizations(context.Background())
				return err
			},
			method: http.MethodGet, path: "/api/v1/instance/organizations/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := organizationReadBody
			if tc.method == http.MethodGet {
				body = "[" + organizationReadBody + "]"
			}
			c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.Method != tc.method || req.URL.Path != tc.path {
						t.Fatalf("request = %s %s, want %s %s", req.Method, req.URL.Path, tc.method, tc.path)
					}
					return &http.Response{
						StatusCode: tc.status,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    req,
					}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.call(c); err != nil {
				t.Fatalf("call failed: %v", err)
			}
		})
	}
}

// TestOrganizationContextExchange pins the exchange request shape and the
// org-context routes authenticating with the exchanged credential.
func TestOrganizationContextExchange(t *testing.T) {
	t.Parallel()
	c, err := NewAPIClient("https://example.invalid/", "admin-key", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodPost && req.URL.Path == "/api/oauth/token":
				form := formBody(req)
				if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
					t.Fatalf("grant_type = %q", form.Get("grant_type"))
				}
				if form.Get("subject_token") != "admin-key" {
					t.Fatalf("subject_token = %q", form.Get("subject_token"))
				}
				if form.Get("subject_token_type") != "urn:ietf:params:oauth:token-type:access_token" {
					t.Fatalf("subject_token_type = %q", form.Get("subject_token_type"))
				}
				if want := "https://example.invalid/acme"; form.Get("audience") != want {
					t.Fatalf("audience = %q, want %q", form.Get("audience"), want)
				}
				if form.Get("scope") != "organization:read organization:write" {
					t.Fatalf("scope = %q", form.Get("scope"))
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(exchangeOKBody)), Request: req}, nil
			case req.Method == http.MethodGet && req.URL.Path == "/api/v1/organization/":
				if auth := req.Header.Get("Authorization"); auth != "Bearer exchanged-token" {
					t.Fatalf("Authorization = %q", auth)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(organizationReadBody)), Request: req}, nil
			default:
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				return nil, nil
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, status, err := c.GetOrganizationContext(context.Background(), "acme")
	if err != nil || status != http.StatusOK {
		t.Fatalf("get failed: status=%d err=%v", status, err)
	}
	if out.OrganizationName != "acme" {
		t.Fatalf("org = %+v", out)
	}
}

// TestOrganizationContextUpdateDelete pins the org-context update and delete
// routes, including that a rename travels in the payload while the audience
// keeps the current name.
func TestOrganizationContextUpdateDelete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		call   func(*APIClient) error
		method string
		body   string
	}{
		{
			name: "update",
			call: func(c *APIClient) error {
				_, err := c.UpdateOrganizationContext(context.Background(), "acme", OrganizationUpdate{})
				return err
			},
			method: http.MethodPut, body: organizationReadBody,
		},
		{
			name: "delete",
			call: func(c *APIClient) error {
				return c.DeleteOrganizationContext(context.Background(), "acme")
			},
			method: http.MethodDelete, body: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewAPIClient("https://example.invalid", "admin-key", &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/api/oauth/token" {
						return exchangeTransport().RoundTrip(req)
					}
					if req.Method != tc.method || req.URL.Path != "/api/v1/organization/" {
						t.Fatalf("request = %s %s", req.Method, req.URL.Path)
					}
					return &http.Response{StatusCode: map[string]int{
						http.MethodPut:    http.StatusOK,
						http.MethodDelete: http.StatusNoContent,
					}[tc.method], Header: make(http.Header),
						Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.call(c); err != nil {
				t.Fatalf("call failed: %v", err)
			}
		})
	}
}

// TestOrganizationContextTokenCaching verifies a second context call within
// the token TTL exchanges only once.
func TestOrganizationContextTokenCaching(t *testing.T) {
	t.Parallel()
	var exchanges atomic.Int64
	c, err := NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/oauth/token" {
				exchanges.Add(1)
				return exchangeTransport().RoundTrip(req)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(organizationReadBody)), Request: req}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := c.GetOrganizationContext(context.Background(), "acme"); err != nil {
			t.Fatalf("get failed: %v", err)
		}
	}
	if exchanges.Load() != 1 {
		t.Fatalf("exchange calls = %d, want 1", exchanges.Load())
	}
}

// TestOrganizationContextInvalidTarget verifies the RFC 6749 envelope from
// the token endpoint is classified as an invalid_target OAuthTokenError.
func TestOrganizationContextInvalidTarget(t *testing.T) {
	t.Parallel()
	c, err := NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"error":"invalid_target","error_description":"unknown organization: 'acme'"}`)), Request: req}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.GetOrganizationContext(context.Background(), "acme")
	if err == nil || !IsInvalidTargetError(err) {
		t.Fatalf("expected invalid_target, got %v", err)
	}
	if err := c.DeleteOrganizationContext(context.Background(), "acme"); err == nil || !IsInvalidTargetError(err) {
		t.Fatalf("expected invalid_target on delete, got %v", err)
	}
}
