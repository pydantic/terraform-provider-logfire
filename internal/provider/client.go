// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

// client.go
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type APIClient struct {
	BaseURL *url.URL
	HTTP    *http.Client
	Token   string
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("API error: status %d", e.StatusCode)
}

func isStatusExpected(statusCode int, expectedStatuses []int) bool {
	// If no expected statuses provided, accept 2xx
	if len(expectedStatuses) == 0 {
		return statusCode >= 200 && statusCode < 300
	}

	for _, expected := range expectedStatuses {
		if statusCode == expected {
			return true
		}
	}

	return false
}

func NewAPIClient(baseURL, token string, httpClient *http.Client) (*APIClient, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}
	return &APIClient{
		BaseURL: u,
		HTTP:    httpClient,
		Token:   token,
	}, nil
}

func (c *APIClient) doJSON(ctx context.Context, method, path string, in any, out any, expectedStatus ...int) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		body = bytes.NewReader(b)
	}

	u := c.BaseURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}

	defer func() {
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	if !isStatusExpected(resp.StatusCode, expectedStatus) {
		b, _ := io.ReadAll(resp.Body)
		return resp, &APIError{
			StatusCode: resp.StatusCode,
			Message:    string(b),
		}
	}

	// Decode response if requested
	if out != nil {
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(out); err != nil && err != io.EOF {
			return resp, fmt.Errorf("decode response: %w", err)
		}
	}

	return resp, nil
}

// ---- Projects ----

// NullableField helps send explicit nulls (to clear values) while still allowing
// us to omit fields for partial updates.
type NullableField[T any] struct {
	Value *T
	set   bool
}

func NullableFieldValue[T any](v T) NullableField[T] {
	return NullableField[T]{Value: &v, set: true}
}

func NullableFieldNull[T any]() NullableField[T] {
	return NullableField[T]{set: true}
}

func (n NullableField[T]) IsZero() bool {
	return !n.set
}

func (n NullableField[T]) MarshalJSON() ([]byte, error) {
	if !n.set || n.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*n.Value)
}

type ProjectRead struct {
	ID               string  `json:"id"`
	ProjectName      string  `json:"project_name"`
	Description      *string `json:"description"`
	Visibility       string  `json:"visibility"`
	OrganizationName string  `json:"organization_name"`
	CreatedAt        string  `json:"created_at"`
}

type ProjectCreate struct {
	ProjectName string  `json:"project_name"`
	Description *string `json:"description,omitempty"`
	Visibility  *string `json:"visibility,omitempty"`
}

type ProjectUpdate struct {
	ProjectName *string               `json:"project_name,omitempty"`
	Description NullableField[string] `json:"description,omitempty"`
	Visibility  NullableField[string] `json:"visibility,omitempty"`
}

func (c *APIClient) projectsBase() string {
	return "api/v1/projects/"
}

func (c *APIClient) projectPath(id string) string {
	return fmt.Sprintf("%s%s/", c.projectsBase(), url.PathEscape(id))
}

func (c *APIClient) CreateProject(ctx context.Context, in ProjectCreate) (*ProjectRead, error) {
	var out ProjectRead
	_, err := c.doJSON(ctx, http.MethodPost, c.projectsBase(), in, &out, http.StatusCreated, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetProject(ctx context.Context, id string) (*ProjectRead, int, error) {
	var out ProjectRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.projectPath(id), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateProject(ctx context.Context, id string, in ProjectUpdate) (*ProjectRead, error) {
	var out ProjectRead
	_, err := c.doJSON(ctx, http.MethodPut, c.projectPath(id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteProject(ctx context.Context, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.projectPath(id), nil, nil, http.StatusNoContent)
	return err
}

func (c *APIClient) ListProjects(ctx context.Context) ([]ProjectRead, error) {
	var out []ProjectRead
	_, err := c.doJSON(ctx, http.MethodGet, c.projectsBase(), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Alerts ----

type AlertRead struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	ProjectID      string          `json:"project_id"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      *string         `json:"updated_at"`
	CreatedByName  *string         `json:"created_by_name"`
	UpdatedByName  *string         `json:"updated_by_name"`
	Name           string          `json:"name"`
	Description    *string         `json:"description"`
	Query          string          `json:"query"`
	TimeWindow     string          `json:"time_window"`
	Frequency      string          `json:"frequency"`
	Watermark      string          `json:"watermark"`
	Channels       []ChannelRead   `json:"channels"`
	NotifyWhen     string          `json:"notify_when"`
	Active         bool            `json:"active"`
	LastRun        *string         `json:"last_run,omitempty"`
	HasMatches     *bool           `json:"has_matches,omitempty"`
	HasErrors      *bool           `json:"has_errors,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	ResultLength   *int            `json:"result_length,omitempty"`
}

type AlertCreate struct {
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Query       string   `json:"query"`
	TimeWindow  string   `json:"time_window"`
	Frequency   string   `json:"frequency"`
	Watermark   string   `json:"watermark"`
	ChannelIDs  []string `json:"channel_ids"`
	NotifyWhen  string   `json:"notify_when"`
}

type AlertUpdate struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	TimeWindow  *string   `json:"time_window,omitempty"`
	Frequency   *string   `json:"frequency,omitempty"`
	Watermark   *string   `json:"watermark,omitempty"`
	Active      *bool     `json:"active,omitempty"`
	Query       *string   `json:"query,omitempty"`
	ChannelIDs  *[]string `json:"channel_ids,omitempty"`
	NotifyWhen  *string   `json:"notify_when,omitempty"`
}

func (c *APIClient) alertsBase(projectID string) string {
	return fmt.Sprintf("api/v1/projects/%s/alerts/", url.PathEscape(projectID))
}
func (c *APIClient) alertPath(projectID, id string) string {
	return fmt.Sprintf("%s%s/", c.alertsBase(projectID), url.PathEscape(id))
}

func (c *APIClient) CreateAlert(ctx context.Context, projectID string, in AlertCreate) (*AlertRead, error) {
	var out AlertRead
	_, err := c.doJSON(ctx, http.MethodPost, c.alertsBase(projectID), in, &out, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetAlert(ctx context.Context, projectID, id string) (*AlertRead, int, error) {
	var out AlertRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.alertPath(projectID, id), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateAlert(ctx context.Context, projectID, id string, in AlertUpdate) (*AlertRead, error) {
	var out AlertRead
	_, err := c.doJSON(ctx, http.MethodPut, c.alertPath(projectID, id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteAlert(ctx context.Context, projectID, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.alertPath(projectID, id), nil, nil, http.StatusNoContent)
	return err
}

// ---- Channels ----

// Base channel config - never used directly, only for embedding.
type ChannelConfigBase struct {
	Type string `json:"type"`
}

type WebhookConfig struct {
	ChannelConfigBase
	Format *string `json:"format,omitempty"`
	URL    *string `json:"url,omitempty"`
}

type EmailConfig struct {
	ChannelConfigBase
	Email *string `json:"email,omitempty"`
}

type OpsgenieConfig struct {
	ChannelConfigBase
	AuthKey *string `json:"auth_key,omitempty"`
}

// ChannelConfig represents any channel config (used for unmarshaling).
type ChannelConfig struct {
	Type    string  `json:"type"`
	Format  *string `json:"format,omitempty"`
	URL     *string `json:"url,omitempty"`
	Email   *string `json:"email,omitempty"`
	AuthKey *string `json:"auth_key,omitempty"`
}

type ChannelRead struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organization_id"`
	Label          string        `json:"label"`
	Active         bool          `json:"active"`
	CreatedAt      string        `json:"created_at"`
	UpdatedAt      *string       `json:"updated_at"`
	CreatedByName  *string       `json:"created_by_name"`
	UpdatedByName  *string       `json:"updated_by_name"`
	Config         ChannelConfig `json:"config"`
}

type ChannelCreate struct {
	Label  string      `json:"label"`
	Config interface{} `json:"config"` // WebhookConfig, EmailConfig, or OpsgenieConfig
}

type ChannelUpdate struct {
	Label  NullableField[string] `json:"label,omitempty"`
	Config *interface{}          `json:"config,omitempty"` // WebhookConfig, EmailConfig, or OpsgenieConfig
	Active NullableField[bool]   `json:"active,omitempty"`
}

func (c *APIClient) channelsBase() string {
	return "/api/v1/channels/"
}
func (c *APIClient) channelPath(id string) string {
	return fmt.Sprintf("/api/v1/channels/%s/", url.PathEscape(id))
}

func (c *APIClient) CreateChannel(ctx context.Context, in ChannelCreate) (*ChannelRead, error) {
	var out ChannelRead
	_, err := c.doJSON(ctx, http.MethodPost, c.channelsBase(), in, &out, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetChannel(ctx context.Context, id string) (*ChannelRead, int, error) {
	var out ChannelRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.channelPath(id), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateChannel(ctx context.Context, id string, in ChannelUpdate) (*ChannelRead, error) {
	var out ChannelRead
	_, err := c.doJSON(ctx, http.MethodPut, c.channelPath(id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteChannel(ctx context.Context, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.channelPath(id), nil, nil, http.StatusNoContent)
	return err
}

// ---- Write Tokens ----

type WriteToken struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	CreatedAt     string  `json:"created_at"`
	Description   *string `json:"description"`
	Token         *string `json:"token,omitempty"`
	ProjectName   string  `json:"project_name"`
	CreatedByName *string `json:"created_by_name"`
	TokenPrefix   string  `json:"token_prefix"`
}

type WriteTokenCreate struct {
	Description *string `json:"description,omitempty"`
}

func (c *APIClient) writeTokensBase(projectID string) string {
	return fmt.Sprintf("api/v1/projects/%s/write-tokens/", url.PathEscape(projectID))
}
func (c *APIClient) writeTokenPath(projectID, tokenID string) string {
	return fmt.Sprintf("%s%s/", c.writeTokensBase(projectID), url.PathEscape(tokenID))
}

func (c *APIClient) CreateWriteToken(ctx context.Context, projectID string, in WriteTokenCreate) (*WriteToken, error) {
	var out WriteToken
	_, err := c.doJSON(ctx, http.MethodPost, c.writeTokensBase(projectID), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) ListWriteTokens(ctx context.Context, projectID string) ([]WriteToken, error) {
	var out []WriteToken
	_, err := c.doJSON(ctx, http.MethodGet, c.writeTokensBase(projectID), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *APIClient) DeleteWriteToken(ctx context.Context, projectID, tokenID string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.writeTokenPath(projectID, tokenID), nil, nil, http.StatusNoContent)
	return err
}

// ---- Dashboards ----

type DashboardCreateRequest struct {
	Name       string          `json:"name"`
	Slug       string          `json:"slug"`
	Definition json.RawMessage `json:"definition"`
}

type DashboardUpdateRequest struct {
	Name       *string          `json:"name,omitempty"`
	Definition *json.RawMessage `json:"definition,omitempty"`
}

type Dashboard struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     *string         `json:"updated_at"`
	CreatedByName *string         `json:"created_by_name"`
	UpdatedByName *string         `json:"updated_by_name"`
	DashboardName string          `json:"dashboard_name"`
	DashboardSlug string          `json:"dashboard_slug"`
	Definition    json.RawMessage `json:"definition"`
}

type GetDashboardResponse struct {
	Dashboard json.RawMessage `json:"dashboard"`
}

func (c *APIClient) dashboardsBase(projectID string) string {
	return fmt.Sprintf("api/v1/projects/%s/dashboards/", url.PathEscape(projectID))
}
func (c *APIClient) dashboardPath(projectID, dashboardID string) string {
	return fmt.Sprintf("%s%s/", c.dashboardsBase(projectID), url.PathEscape(dashboardID))
}

func (c *APIClient) CreateDashboard(ctx context.Context, projectID string, in DashboardCreateRequest) (*Dashboard, error) {
	var out Dashboard
	_, err := c.doJSON(ctx, http.MethodPost, c.dashboardsBase(projectID), in, &out, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetDashboard(ctx context.Context, projectID, dashboardID string) (*GetDashboardResponse, int, error) {
	var out GetDashboardResponse
	resp, err := c.doJSON(ctx, http.MethodGet, c.dashboardPath(projectID, dashboardID), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateDashboard(ctx context.Context, projectID, dashboardID string, in DashboardUpdateRequest) (*Dashboard, error) {
	var out Dashboard
	_, err := c.doJSON(ctx, http.MethodPut, c.dashboardPath(projectID, dashboardID), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteDashboard(ctx context.Context, projectID, dashboardID string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.dashboardPath(projectID, dashboardID), nil, nil, http.StatusNoContent)
	return err
}
