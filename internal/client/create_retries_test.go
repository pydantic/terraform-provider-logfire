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
	"time"
)

func TestNonIdempotentCreatesDoNotRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		call func(*APIClient) error
	}{
		{"organization", func(c *APIClient) error {
			_, err := c.CreateOrganization(context.Background(), OrganizationCreate{})
			return err
		}},
		{"project", func(c *APIClient) error { _, err := c.CreateProject(context.Background(), ProjectCreate{}); return err }},
		{"alert", func(c *APIClient) error {
			_, err := c.CreateAlert(context.Background(), "project", AlertCreate{})
			return err
		}},
		{"channel", func(c *APIClient) error { _, err := c.CreateChannel(context.Background(), ChannelCreate{}); return err }},
		{"read token", func(c *APIClient) error {
			_, err := c.CreateReadToken(context.Background(), "project", CreateReadTokenInput{})
			return err
		}},
		{"write token", func(c *APIClient) error {
			_, err := c.CreateWriteToken(context.Background(), "project", CreateWriteTokenInput{})
			return err
		}},
		{"dashboard", func(c *APIClient) error {
			_, err := c.CreateDashboard(context.Background(), "project", DashboardCreateRequest{})
			return err
		}},
		{"SLO", func(c *APIClient) error {
			_, err := c.CreateSlo(context.Background(), "project", SloCreate{})
			return err
		}},
		{"API key", func(c *APIClient) error { _, err := c.CreateAPIKey(context.Background(), APIKeyCreate{}); return err }},
		{"gateway provider", func(c *APIClient) error {
			_, err := c.CreateGatewayProvider(context.Background(), GatewayProviderCreate{})
			return err
		}},
		{"frontend application", func(c *APIClient) error {
			_, err := c.CreateFrontendApplication(context.Background(), "project", FrontendApplicationCreate{})
			return err
		}},
		{"frontend token", func(c *APIClient) error {
			_, err := c.CreateFrontendApplicationToken(context.Background(), "project", "application")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			created := 0
			c := newAPIKeyTestClient(t, &retryingTransport{
				next: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					created++
					status, body := http.StatusServiceUnavailable, "response lost after creation"
					if created > 1 {
						status, body = http.StatusCreated, `{"id":"duplicate","token":"second-token"}`
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Retry-After": []string{"0"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    req,
					}, nil
				}),
				maxAttempts: 2,
				baseDelay:   time.Millisecond,
				maxDelay:    time.Millisecond,
			})
			err := tc.call(c)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("create error = %v; want initial HTTP 503", err)
			}
			if created != 1 {
				t.Errorf("created resources = %d; want 1", created)
			}
		})
	}
}
