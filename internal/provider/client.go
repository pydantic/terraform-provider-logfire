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
	"time"
)

type APIClient struct {
	BaseURL     *url.URL
	HTTP        *http.Client
	Token       string
	Organization string
	Project      string
}

func NewAPIClient(baseURL, token, org, project string, httpClient *http.Client) (*APIClient, error) {
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
		BaseURL:      u,
		HTTP:         httpClient,
		Token:        token,
		Organization: org,
		Project:      project,
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
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	
	defer func() {
		// Drain but preserve body for callers that read into out
		if out == nil {
			io.Copy(io.Discard, resp.Body)
		}
	}()

	// status check
	ok := false
	for _, s := range expectedStatus {
		if resp.StatusCode == s {
			ok = true
			break
		}
	}
	if !ok {
		// try to read server error
		b, _ := io.ReadAll(resp.Body)
		return resp, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
	}

	if out != nil {
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(out); err != nil && err != io.EOF {
			return resp, err
		}
	}
	return resp, nil
}

// ---- Alerts model (only what we need) ----

type AlertRead struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Query       string   `json:"query"`
	TimeWindow  string   `json:"time_window"` // "PT5M", "PT30S"
	Frequency   string   `json:"frequency"`
	Watermark   string   `json:"watermark"`
	ChannelIDs  []string `json:"channel_ids"`
	NotifyWhen  string   `json:"notify_when"`
	Active      bool     `json:"active"`
}

type AlertCreate struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Query       string   `json:"query"`
	TimeWindow  string   `json:"time_window"` // ISO-8601
	Frequency   string   `json:"frequency"`
	Watermark   string  `json:"watermark"`
	ChannelIDs  []string `json:"channel_ids"`
	NotifyWhen  string   `json:"notify_when"`
}

type AlertUpdate struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	TimeWindow  *string  `json:"time_window,omitempty"` // ISO-8601
	Frequency   *string  `json:"frequency,omitempty"`
	Watermark   *string  `json:"watermark,omitempty"`
	Active      *bool     `json:"active,omitempty"`
	Query       *string   `json:"query,omitempty"`
	ChannelIDs  *[]string `json:"channel_ids,omitempty"`
	NotifyWhen  *string   `json:"notify_when,omitempty"`
}

func secs(d time.Duration) float64 { return d.Seconds() }

// API path helpers
func (c *APIClient) alertsBase() string {
	return fmt.Sprintf("api/organizations/%s/projects/%s/alerts/", url.PathEscape(c.Organization), url.PathEscape(c.Project))
}
func (c *APIClient) alertPath(id string) string {
	return fmt.Sprintf("%s%s", c.alertsBase(), url.PathEscape(id))
}

// ---- API methods ----

func (c *APIClient) CreateAlert(ctx context.Context, in AlertCreate) (*AlertRead, error) {
	var out AlertRead
	_, err := c.doJSON(ctx, http.MethodPost, c.alertsBase(), in, &out, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetAlert(ctx context.Context, id string) (*AlertRead, int, error) {
	var out AlertRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.alertPath(id), nil, &out, http.StatusOK)
	if err != nil {
		// bubble status for 404 handling
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateAlert(ctx context.Context, id string, in AlertUpdate) (*AlertRead, error) {
	var out AlertRead
	_, err := c.doJSON(ctx, http.MethodPut, c.alertPath(id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteAlert(ctx context.Context, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.alertPath(id), nil, nil, http.StatusNoContent)
	return err
}
