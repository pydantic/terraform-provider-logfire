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
			return nil, err
		}
		body = bytes.NewReader(b)
	}

	u := c.BaseURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	// Always drain and close so the connection can be reused.
	defer func() {
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body) // explicitly ignore error
			_ = resp.Body.Close()
		}
	}()

	// Check status
	ok := len(expectedStatus) == 0 // if none provided, treat 2xx as OK below
	if !ok {
		for _, s := range expectedStatus {
			if resp.StatusCode == s {
				ok = true
				break
			}
		}
	}
	if !ok {
		// If caller didn't supply expected statuses, fall back to 2xx.
		if len(expectedStatus) == 0 && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			ok = true
		}
	}
	if !ok {
		b, _ := io.ReadAll(resp.Body) // best-effort read for error message
		return resp, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
	}

	// Decode response if requested
	if out != nil {
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(out); err != nil && err != io.EOF {
			return resp, err
		}
	}

	return resp, nil
}

// ---- Alerts ----

type AlertRead struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Query       string        `json:"query"`
	TimeWindow  string        `json:"time_window"`
	Frequency   string        `json:"frequency"`
	Watermark   string        `json:"watermark"`
	ChannelIDs  []string      `json:"channel_ids"`
	Channels    []ChannelRead `json:"channels"`
	NotifyWhen  string        `json:"notify_when"`
	Active      bool          `json:"active"`
}

type AlertCreate struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
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

func (c *APIClient) alertsBase(org, project string) string {
	return fmt.Sprintf("api/organizations/%s/projects/%s/alerts/", url.PathEscape(org), url.PathEscape(project))
}
func (c *APIClient) alertPath(org, project, id string) string {
	return fmt.Sprintf("%s%s/", c.alertsBase(org, project), url.PathEscape(id))
}

func (c *APIClient) CreateAlert(ctx context.Context, org, project string, in AlertCreate) (*AlertRead, error) {
	var out AlertRead
	_, err := c.doJSON(ctx, http.MethodPost, c.alertsBase(org, project), in, &out, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetAlert(ctx context.Context, org, project, id string) (*AlertRead, int, error) {
	var out AlertRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.alertPath(org, project, id), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateAlert(ctx context.Context, org, project, id string, in AlertUpdate) (*AlertRead, error) {
	var out AlertRead
	_, err := c.doJSON(ctx, http.MethodPut, c.alertPath(org, project, id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteAlert(ctx context.Context, org, project, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.alertPath(org, project, id), nil, nil, http.StatusNoContent)
	return err
}

// ---- Channels ----

type ChannelConfig struct {
	Type   string `json:"type"`
	Format string `json:"format"`
	URL    string `json:"url"`
}

type ChannelRead struct {
	ID     string        `json:"id"`
	Label  string        `json:"label"`
	Config ChannelConfig `json:"config"`
}

type ChannelCreate struct {
	Label  string        `json:"label"`
	Config ChannelConfig `json:"config"`
}

type ChannelUpdate struct {
	Label  *string        `json:"label,omitempty"`
	Config *ChannelConfig `json:"config,omitempty"`
}

func (c *APIClient) channelsBase(org, project string) string {
	return fmt.Sprintf("api/organizations/%s/projects/%s/channels/", url.PathEscape(org), url.PathEscape(project))
}
func (c *APIClient) channelPath(org, project, id string) string {
	return fmt.Sprintf("%s%s/", c.channelsBase(org, project), url.PathEscape(id))
}

func (c *APIClient) CreateChannel(ctx context.Context, org, project string, in ChannelCreate) (*ChannelRead, error) {
	var out ChannelRead
	_, err := c.doJSON(ctx, http.MethodPost, c.channelsBase(org, project), in, &out, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetChannel(ctx context.Context, org, project, id string) (*ChannelRead, int, error) {
	var out ChannelRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.channelPath(org, project, id), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateChannel(ctx context.Context, org, project, id string, in ChannelUpdate) (*ChannelRead, error) {
	var out ChannelRead
	_, err := c.doJSON(ctx, http.MethodPut, c.channelPath(org, project, id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteChannel(ctx context.Context, org, project, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.channelPath(org, project, id), nil, nil, http.StatusNoContent)
	return err
}

// ---- Projects ----

type ProjectRead struct {
	ID          string `json:"id"`
	ProjectName string `json:"project_name"`
	Description string `json:"description"`
}

type ProjectCreate struct {
	ProjectName string `json:"project_name"`
	Description string `json:"description"`
}

type ProjectUpdate struct {
	ProjectName *string `json:"project_name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (c *APIClient) projectsBase(org string) string {
	return fmt.Sprintf("api/organizations/%s/projects/", url.PathEscape(org))
}
func (c *APIClient) projectPath(org, id string) string {
	return fmt.Sprintf("%s%s", c.projectsBase(org), url.PathEscape(id))
}

func (c *APIClient) CreateProject(ctx context.Context, org string, in ProjectCreate) (*ProjectRead, error) {
	var out ProjectRead
	_, err := c.doJSON(ctx, http.MethodPost, c.projectsBase(org), in, &out, http.StatusCreated, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetProject(ctx context.Context, org, id string) (*ProjectRead, int, error) {
	var out ProjectRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.projectPath(org, id), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateProject(ctx context.Context, org, id string, in ProjectUpdate) (*ProjectRead, error) {
	var out ProjectRead
	_, err := c.doJSON(ctx, http.MethodPut, c.projectPath(org, id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteProject(ctx context.Context, org, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.projectPath(org, id), nil, nil, http.StatusNoContent)
	return err
}
