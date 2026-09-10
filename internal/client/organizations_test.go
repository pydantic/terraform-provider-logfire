// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type organizationPathTransport struct {
	status int
	body   string
	check  func(*http.Request) error
}

func (t organizationPathTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.check(req); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: t.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

const organizationReadBody = `{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","organization_name":"acme",
	"subscription_plan":null,"has_admin_panel":true,"created_at":"2026-01-01T00:00:00Z",
	"updated_at":"2026-01-01T00:00:00Z","billing_email":null,"organization_display_name":null,
	"github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
	"spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
	"gateway_enabled":false,"ai_enabled":false}`

// TestOrganizationClientRoutes pins the create/list migration to the instance
// routes and the still-legacy read/update/delete routes.
func TestOrganizationClientRoutes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		call   func(*APIClient) error
		method string
		path   string
		inBody bool
	}{
		{
			name:   "create uses instance route",
			status: http.StatusCreated,
			call: func(c *APIClient) error {
				_, err := c.CreateOrganization(context.Background(), OrganizationCreate{OrganizationName: "acme"})
				return err
			},
			method: http.MethodPost, path: "/api/v1/instance/organizations/", inBody: true,
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
		{
			name:   "get stays on legacy route",
			status: http.StatusOK,
			call: func(c *APIClient) error {
				_, _, err := c.GetOrganization(context.Background(), "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff")
				return err
			},
			method: http.MethodGet, path: "/api/v1/organizations/9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff/",
		},
		{
			name:   "update stays on legacy route",
			status: http.StatusOK,
			call: func(c *APIClient) error {
				_, err := c.UpdateOrganization(context.Background(), "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff", OrganizationUpdate{})
				return err
			},
			method: http.MethodPut, path: "/api/v1/organizations/9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff/", inBody: true,
		},
		{
			name:   "delete stays on legacy route",
			status: http.StatusNoContent,
			call: func(c *APIClient) error {
				return c.DeleteOrganization(context.Background(), "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff")
			},
			method: http.MethodDelete, path: "/api/v1/organizations/9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
				Transport: organizationPathTransport{
					status: tc.status,
					// The list route returns an array; the others return one object.
					body: func() string {
						if tc.method == http.MethodGet && strings.HasSuffix(tc.path, "organizations/") {
							return "[" + organizationReadBody + "]"
						}
						return organizationReadBody
					}(),
					check: func(req *http.Request) error {
						if req.Method != tc.method {
							t.Fatalf("method = %s, want %s", req.Method, tc.method)
						}
						if req.URL.Path != tc.path {
							t.Fatalf("path = %s, want %s", req.URL.Path, tc.path)
						}
						return nil
					},
				},
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
