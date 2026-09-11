// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// versionedStubTransport answers every request with one status and body,
// optionally adding the x-backend-version header.
type versionedStubTransport struct {
	status  int
	body    string
	version string
}

func (t versionedStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	if t.version != "" {
		header.Set("x-backend-version", t.version)
	}
	return &http.Response{
		StatusCode: t.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

func TestListGatewayProvidersMissingRouteHint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		version string
	}{
		{name: "with reported version", version: "e79656b9"},
		{name: "without reported version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
				Transport: versionedStubTransport{
					status:  http.StatusNotFound,
					body:    `{"detail":"Not Found"}`,
					version: tc.version,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.ListGatewayProviders(context.Background())
			var unavailable *EndpointUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("expected EndpointUnavailableError, got %v", err)
			}
			message := err.Error()
			for _, want := range []string{"v2026-08-12.01", "logfire-0.13.40", "/api/v1/gateway/providers/"} {
				if !strings.Contains(message, want) {
					t.Fatalf("error %q does not mention %q", message, want)
				}
			}
			if tc.version != "" && !strings.Contains(message, tc.version) {
				t.Fatalf("error %q does not report instance version %q", message, tc.version)
			}
			if tc.version == "" && strings.Contains(message, "reports version") {
				t.Fatalf("error %q must not claim a version when none was reported", message)
			}
		})
	}
}

func TestFindGatewayProviderBySlugMissingSlugIsNotRouteHint(t *testing.T) {
	t.Parallel()
	c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
		Transport: versionedStubTransport{
			status: http.StatusOK,
			body:   `{"providers":[],"next_cursor":null}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.FindGatewayProviderBySlug(context.Background(), "missing")
	if !IsNotFoundError(err) {
		t.Fatalf("expected a not-found error for the missing slug, got %v", err)
	}
	var unavailable *EndpointUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("a missing slug must not be reported as a missing route: %v", err)
	}
}

func TestListOrganizationsMissingRouteHint(t *testing.T) {
	t.Parallel()
	c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
		Transport: versionedStubTransport{
			status: http.StatusNotFound,
			body:   `{"detail":"Not Found"}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListOrganizations(context.Background())
	var unavailable *EndpointUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected EndpointUnavailableError, got %v", err)
	}
	if !strings.Contains(err.Error(), "v2026-06-03.01") {
		t.Fatalf("error %q does not mention the instance organization floor", err)
	}
}

func TestAPIErrorCapturesBackendVersion(t *testing.T) {
	t.Parallel()
	c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
		Transport: versionedStubTransport{
			status:  http.StatusInternalServerError,
			body:    `{"detail":"boom"}`,
			version: "abc12345",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListOrganizations(context.Background())
	if err == nil || IsNotFoundError(err) {
		t.Fatalf("expected a non-404 error, got %v", err)
	}
	if got := BackendVersionFromError(err); got != "abc12345" {
		t.Fatalf("BackendVersion = %q, want abc12345", got)
	}
}
