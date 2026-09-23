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
// optionally adding the Logfire-Version header.
type versionedStubTransport struct {
	status  int
	body    string
	version string
}

func (t versionedStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	if t.version != "" {
		header.Set("Logfire-Version", t.version)
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
		name string
		// version is the Logfire-Version header value the stub returns.
		version string
		// reported is the release the message must quote, empty when the
		// header carries no version meaning and nothing may be quoted.
		reported string
	}{
		{name: "with reported release", version: "v2026-09-14.01", reported: "v2026-09-14.01"},
		{name: "with build identity only", version: "e79656b9"},
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
			if tc.reported != "" && !strings.Contains(message, tc.reported) {
				t.Fatalf("error %q does not report instance version %q", message, tc.reported)
			}
			if tc.reported == "" {
				if strings.Contains(message, "reports version") {
					t.Fatalf("error %q must not claim a version when none was reported", message)
				}
				if tc.version != "" && strings.Contains(message, tc.version) {
					t.Fatalf("error %q must not quote a build identity as a version", message)
				}
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

func TestListAPIKeysMissingRouteHint(t *testing.T) {
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
	_, err = c.ListAPIKeys(context.Background())
	var unavailable *EndpointUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected EndpointUnavailableError, got %v", err)
	}
	if !strings.Contains(err.Error(), "v2026-06-09.01") {
		t.Fatalf("error %q does not mention the API-keys floor", err)
	}
}

func TestCreateScheduleMissingRouteHint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// version is the Logfire-Version header value the stub returns.
		version string
		// reported is the release the message must quote, empty when the
		// header carries no version meaning and nothing may be quoted.
		reported string
	}{
		{name: "with reported release", version: "v2026-09-18.01", reported: "v2026-09-18.01"},
		{name: "with build identity only", version: "e79656b9"},
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
			_, err = c.CreateSchedule(context.Background(), ScheduleCreate{Label: "example", Timezone: "UTC"})
			var unavailable *EndpointUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("expected EndpointUnavailableError, got %v", err)
			}
			message := err.Error()
			for _, want := range []string{"v2026-09-23.01", "logfire-0.13.47", "/api/v1/schedules/"} {
				if !strings.Contains(message, want) {
					t.Fatalf("error %q does not mention %q", message, want)
				}
			}
			if tc.reported != "" && !strings.Contains(message, tc.reported) {
				t.Fatalf("error %q does not report instance version %q", message, tc.reported)
			}
			if tc.reported == "" {
				if strings.Contains(message, "reports version") {
					t.Fatalf("error %q must not claim a version when none was reported", message)
				}
				if tc.version != "" && strings.Contains(message, tc.version) {
					t.Fatalf("error %q must not quote a build identity as a version", message)
				}
			}
		})
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
			c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
				Transport: versionedStubTransport{
					status:  http.StatusInternalServerError,
					body:    `{"detail":"boom"}`,
					version: tc.version,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.ListOrganizations(context.Background())
			if err == nil || IsNotFoundError(err) {
				t.Fatalf("expected a non-404 error, got %v", err)
			}
			if got := BackendVersionFromError(err); got != tc.want {
				t.Fatalf("BackendVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
