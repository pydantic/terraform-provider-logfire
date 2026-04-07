// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
	}
	if okVal, ok := out["ok"]; !ok || !okVal {
		t.Fatalf("expected decoded ok response, got %+v", out)
	}
}

type recordingTransport struct {
	attempts       int
	bodies         []string
	userAgents     []string
	versionHeaders []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	rt.bodies = append(rt.bodies, string(data))
	rt.userAgents = append(rt.userAgents, req.Header.Get("User-Agent"))
	rt.versionHeaders = append(rt.versionHeaders, req.Header.Get("X-Terraform-Provider-Version"))

	rt.attempts++
	if rt.attempts == 1 {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header: http.Header{
				"Retry-After": []string{"0"},
			},
			Body:    io.NopCloser(strings.NewReader("rate limited")),
			Request: req,
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}
