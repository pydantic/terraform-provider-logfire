// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stubResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func newStubClient(t *testing.T, transport roundTripFunc) *APIClient {
	t.Helper()
	c, err := NewAPIClient("https://example.invalid", "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newServerClient(t *testing.T, handler http.HandlerFunc) *APIClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewAPIClient(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func requireRequest(t *testing.T, req *http.Request, method, path string) {
	t.Helper()
	if req.Method != method || req.URL.Path != path {
		t.Errorf("request = %s %s, want %s %s", req.Method, req.URL.Path, method, path)
	}
}

func TestErrorClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		is   func(error) bool
		err  error
		want bool
	}{
		{"409 is a conflict", IsConflictError, &APIError{StatusCode: http.StatusConflict}, true},
		{"non-API error is not a conflict", IsConflictError, errors.New("other"), false},
		{"404 is not found", IsNotFoundError, &APIError{StatusCode: http.StatusNotFound}, true},
		{"500 is not a not-found", IsNotFoundError, &APIError{StatusCode: http.StatusInternalServerError}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.is(tc.err); got != tc.want {
				t.Fatalf("classified %v as %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryingTransport_ReplaysBodyAndSetsHeaders(t *testing.T) {
	rt := &recordingTransport{}

	transport := &retryingTransport{
		next:        rt,
		maxAttempts: 3,
		baseDelay:   time.Millisecond,
		maxDelay:    2 * time.Millisecond,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}

	apiClient, err := NewAPIClient(
		"https://example.com",
		"token",
		httpClient,
		WithUserAgent("terraform-provider-logfire/test"),
		WithAdditionalHeaders(http.Header{
			"X-Terraform-Provider-Version": []string{"test-version"},
		}),
		WithAdditionalHeaders(http.Header{
			"Example-Header": []string{"first-value"},
		}),
		WithAdditionalHeaders(http.Header{
			"Example-Header": []string{"second-value"},
		}),
	)
	if err != nil {
		t.Fatalf("NewAPIClient: %v", err)
	}

	var out map[string]bool
	_, err = apiClient.doJSON(context.Background(), http.MethodPost, "/example", map[string]string{"hello": "world"}, &out, http.StatusOK)
	if err != nil {
		t.Fatalf("doJSON: %v", err)
	}

	if len(rt.bodies) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(rt.bodies))
	}
	if rt.bodies[0] != rt.bodies[1] {
		t.Fatalf("request body changed across retries: %q vs %q", rt.bodies[0], rt.bodies[1])
	}
	for i, ua := range rt.userAgents {
		if ua != "terraform-provider-logfire/test" {
			t.Fatalf("attempt %d user agent = %q; want terraform-provider-logfire/test", i+1, ua)
		}
		if rt.versionHeaders[i] != "test-version" {
			t.Fatalf("attempt %d version header = %q; want test-version", i+1, rt.versionHeaders[i])
		}
		if rt.customHeaders[i] != "second-value" {
			t.Fatalf("attempt %d custom header = %q; want second-value", i+1, rt.customHeaders[i])
		}
		if len(rt.customHeaderValues[i]) != 1 {
			t.Fatalf("attempt %d custom header values = %q; want exactly one value", i+1, rt.customHeaderValues[i])
		}
	}
	if okVal, ok := out["ok"]; !ok || !okVal {
		t.Fatalf("expected decoded ok response, got %+v", out)
	}
}

type recordingTransport struct {
	attempts           int
	bodies             []string
	userAgents         []string
	versionHeaders     []string
	customHeaders      []string
	customHeaderValues [][]string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	rt.bodies = append(rt.bodies, string(data))
	rt.userAgents = append(rt.userAgents, req.Header.Get("User-Agent"))
	rt.versionHeaders = append(rt.versionHeaders, req.Header.Get("X-Terraform-Provider-Version"))
	rt.customHeaders = append(rt.customHeaders, req.Header.Get("Example-Header"))
	rt.customHeaderValues = append(rt.customHeaderValues, req.Header.Values("Example-Header"))

	rt.attempts++
	if rt.attempts == 1 {
		resp := stubResponse(req, http.StatusTooManyRequests, "rate limited")
		resp.Header.Set("Retry-After", "0")
		return resp, nil
	}
	return stubResponse(req, http.StatusOK, `{"ok":true}`), nil
}

func TestCreatesAreNotAutomaticallyRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, call := range map[string]func(*APIClient) error{
		"schedule": func(c *APIClient) error {
			_, err := c.CreateSchedule(ctx, ScheduleCreate{Label: "office hours", Timezone: "UTC"})
			return err
		},
		"frontend application": func(c *APIClient) error {
			_, err := c.CreateFrontendApplication(ctx, "project-id", FrontendApplicationCreate{Name: "browser"})
			return err
		},
		"frontend application token": func(c *APIClient) error {
			_, err := c.CreateFrontendApplicationToken(ctx, "project-id", "app-id")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			attempts := 0
			c, err := NewAPIClient("https://example.com", "test", &http.Client{Transport: &retryingTransport{
				maxAttempts: 3,
				next: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					attempts++
					if attempts > 1 {
						return stubResponse(req, http.StatusCreated, ""), nil
					}
					resp := stubResponse(req, http.StatusServiceUnavailable, "")
					resp.Header.Set("Retry-After", "0")
					return resp, nil
				}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := call(c); err == nil {
				t.Fatal("expected the first 503 response to be returned")
			}
			if attempts != 1 {
				t.Fatalf("create request attempts = %d; want 1", attempts)
			}
		})
	}
}

// A request path with an empty ID segment would reach a collection route
// instead of the resource, so the client refuses it before sending.
func TestEmptyResourceIDNeverReachesTheAPI(t *testing.T) {
	var requests atomic.Int32
	c := newServerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	ctx := t.Context()
	calls := map[string]func() error{
		"delete channel": func() error { return c.DeleteChannel(ctx, "") },
		"delete alert":   func() error { return c.DeleteAlert(ctx, "project-1", "") },
		"update project": func() error {
			_, err := c.UpdateProject(ctx, "", ProjectUpdate{})
			return err
		},
		"get schedule": func() error {
			_, _, err := c.GetSchedule(ctx, "")
			return err
		},
		// An organization-scoped call refuses before it exchanges a token.
		"org delete project": func() error { return c.ForOrganization("acme").DeleteProject(ctx, "") },
	}
	for name, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "the resource ID is missing") {
			t.Errorf("%s: err = %v, want missing resource ID", name, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("empty IDs sent %d requests", requests.Load())
	}
}
