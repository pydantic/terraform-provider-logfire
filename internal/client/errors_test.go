// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// versionedClient answers every request with one status and body, adding the
// Logfire-Version header when version is set.
func versionedClient(t *testing.T, status int, body, version string) *APIClient {
	t.Helper()
	return newStubClient(t, func(req *http.Request) (*http.Response, error) {
		resp := stubResponse(req, status, body)
		if version != "" {
			resp.Header.Set("Logfire-Version", version)
		}
		return resp, nil
	})
}

func TestMissingRouteHints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	listProviders := func(c *APIClient) error {
		_, err := c.ListGatewayProviders(ctx)
		return err
	}
	createSchedule := func(c *APIClient) error {
		_, err := c.CreateSchedule(ctx, ScheduleCreate{Label: "example", Timezone: "UTC"})
		return err
	}
	providerWants := []string{"v2026-08-12.01", "logfire-0.13.40", "/api/v1/gateway/providers/"}
	scheduleWants := []string{"v2026-09-23.01", "logfire-0.13.47", "/api/v1/notification-windows/"}
	for _, tc := range []struct {
		name string
		call func(*APIClient) error
		// version is the Logfire-Version header value the stub returns.
		version string
		wants   []string
		// reported is the text the message must quote, empty when the
		// header carries no version meaning and nothing may be quoted.
		reported string
	}{
		{"providers with reported release", listProviders, "v2026-09-14.01", providerWants, "v2026-09-14.01"},
		{"providers with build identity only", listProviders, "e79656b9", providerWants, ""},
		{"providers without reported version", listProviders, "", providerWants, ""},
		{"schedule with reported release", createSchedule, "v2026-09-18.01", scheduleWants, "v2026-09-18.01"},
		{"schedule with build identity only", createSchedule, "e79656b9", scheduleWants, ""},
		{"schedule without reported version", createSchedule, "", scheduleWants, ""},
		{
			name: "organizations",
			call: func(c *APIClient) error {
				_, err := c.ListOrganizations(ctx)
				return err
			},
			wants: []string{"v2026-06-03.01"},
		},
		{
			name: "api keys",
			call: func(c *APIClient) error {
				_, err := c.ListAPIKeys(ctx)
				return err
			},
			wants: []string{"v2026-06-09.01"},
		},
		{
			name: "gateway endpoint create",
			call: func(c *APIClient) error {
				_, err := c.CreateGatewayEndpoint(ctx, GatewayEndpointCreate{Slug: "default"})
				return err
			},
			version:  "v2026-10-29.01",
			wants:    []string{"v2026-10-29.02", "/api/v1/gateway/endpoints/"},
			reported: "reports version v2026-10-29.01",
		},
		{
			name: "gateway endpoint find",
			call: func(c *APIClient) error {
				_, err := c.FindGatewayEndpointBySlug(ctx, "default")
				return err
			},
			version:  "v2026-10-29.01",
			wants:    []string{"v2026-10-29.02", "/api/v1/gateway/endpoints/"},
			reported: "reports version v2026-10-29.01",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.call(versionedClient(t, http.StatusNotFound, `{"detail":"Not Found"}`, tc.version))
			var unavailable *EndpointUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("expected EndpointUnavailableError, got %v", err)
			}
			message := err.Error()
			for _, want := range append([]string{tc.reported}, tc.wants...) {
				if !strings.Contains(message, want) {
					t.Fatalf("error %q does not mention %q", message, want)
				}
			}
			if tc.reported != "" {
				return
			}
			if strings.Contains(message, "reports version") {
				t.Fatalf("error %q must not claim a version when none was reported", message)
			}
			if tc.version != "" && strings.Contains(message, tc.version) {
				t.Fatalf("error %q must not quote a build identity as a version", message)
			}
		})
	}
}

func TestFindGatewayProviderBySlugMissingSlugIsNotRouteHint(t *testing.T) {
	t.Parallel()
	c := versionedClient(t, http.StatusOK, `{"providers":[],"next_cursor":null}`, "")
	_, err := c.FindGatewayProviderBySlug(context.Background(), "missing")
	if !IsNotFoundError(err) {
		t.Fatalf("expected a not-found error for the missing slug, got %v", err)
	}
	var unavailable *EndpointUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("a missing slug must not be reported as a missing route: %v", err)
	}
}

func TestGetGatewayEndpointKeepsItemNotFound(t *testing.T) {
	t.Parallel()
	c := versionedClient(t, http.StatusNotFound, `{"detail":"Endpoint not found."}`, "")
	if _, err := c.GetGatewayEndpoint(context.Background(), "missing"); !IsNotFoundError(err) {
		t.Fatalf("an item 404 must stay a not-found error so a deleted endpoint leaves state, got %v", err)
	}
}

func TestAPIErrorCapturesBackendVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{name: "release tag", version: "v2026-09-14.01", want: "v2026-09-14.01"},
		{name: "build identity is unknown", version: "abc12345", want: ""},
		{name: "no header", version: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := versionedClient(t, http.StatusInternalServerError, `{"detail":"boom"}`, tc.version)
			_, err := c.ListOrganizations(context.Background())
			if err == nil || IsNotFoundError(err) {
				t.Fatalf("expected a non-404 error, got %v", err)
			}
			if got := BackendVersionFromError(err); got != tc.want {
				t.Fatalf("BackendVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
