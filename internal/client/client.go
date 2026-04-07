// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

// Package client provides the HTTP client and Logfire domain types.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestTimeout        = 90 * time.Second
	defaultRequestsPerMinute     = 50
	defaultRequestsPerHour       = 1000
	defaultRetryMaxAttempts      = 5
	defaultRetryBaseDelay        = 500 * time.Millisecond
	defaultRetryMaxDelay         = 15 * time.Second
	defaultMaxIdleConns          = 50
	defaultMaxIdleConnsPerHost   = 10
	defaultMaxConnsPerHost       = 10
	defaultIdleConnTimeout       = 90 * time.Second
	defaultResponseHeaderTimeout = 60 * time.Second
	defaultUserAgent             = "terraform-provider-logfire"
	maxErrorBodySize             = 8192 // 8KB limit for error response bodies
)

var (
	defaultLimiter = newRateLimiter(defaultRequestsPerMinute, defaultRequestsPerHour)
	jitterRand     = rand.New(rand.NewSource(time.Now().UnixNano()))
	jitterMu       sync.Mutex
)

type rateLimitedTransport struct {
	next    http.RoundTripper
	limiter *rateLimiter
}

func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.limiter != nil {
		if err := t.limiter.Wait(req.Context()); err != nil {
			return nil, err
		}
	}
	return t.next.RoundTrip(req)
}

type retryingTransport struct {
	next        http.RoundTripper
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

func (t *retryingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempts := t.maxAttempts
	if attempts <= 0 {
		attempts = 1
	}

	var resp *http.Response
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			switch {
			case req.GetBody != nil:
				body, getErr := req.GetBody()
				if getErr != nil {
					return nil, getErr
				}
				req.Body = body
			case req.Body != nil:
				return nil, fmt.Errorf("cannot retry request with body: GetBody is nil")
			}
		}

		resp, err = t.next.RoundTrip(req)

		retry, delay := t.shouldRetry(req.Context(), resp, err, attempt, attempts)
		if !retry {
			return resp, err
		}

		drainResponse(resp)

		if err := waitWithContext(req.Context(), delay); err != nil {
			return nil, err
		}
	}

	return resp, err
}

func (t *retryingTransport) shouldRetry(ctx context.Context, resp *http.Response, err error, attempt, maxAttempts int) (bool, time.Duration) {
	if attempt >= maxAttempts-1 {
		return false, 0
	}

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return false, 0
		}

		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return true, retryDelay(nil, t.baseDelay, t.maxDelay, attempt)
		}

		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return true, retryDelay(nil, t.baseDelay, t.maxDelay, attempt)
		}

		return false, 0
	}

	if resp == nil {
		return false, 0
	}

	status := resp.StatusCode
	if status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout ||
		(status >= 500 && status < 600) {
		return true, retryDelay(resp, t.baseDelay, t.maxDelay, attempt)
	}

	return false, 0
}

func drainResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func waitWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(resp *http.Response, base, maxDelay time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	if maxDelay <= 0 {
		maxDelay = 15 * time.Second
	}

	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if seconds, err := strconv.Atoi(ra); err == nil && seconds >= 0 {
				return time.Duration(seconds) * time.Second
			}
			if t, err := http.ParseTime(ra); err == nil {
				if delay := time.Until(t); delay > 0 {
					return delay
				}
			}
		}
	}

	delay := base * time.Duration(1<<attempt)
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add jitter to avoid thundering herd on retries.
	jitterMu.Lock()
	jitter := time.Duration(jitterRand.Int63n(int64(delay / 2)))
	jitterMu.Unlock()
	return delay/2 + jitter
}

type rateLimiter struct {
	perMinute *rateGate
	perHour   *rateGate
}

func newRateLimiter(requestsPerMinute, requestsPerHour int) *rateLimiter {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 1
	}
	if requestsPerHour <= 0 {
		requestsPerHour = 1
	}

	minuteInterval := time.Minute / time.Duration(requestsPerMinute)
	hourInterval := time.Hour / time.Duration(requestsPerHour)
	minuteBurst := requestsPerMinute
	hourBurst := requestsPerHour

	return &rateLimiter{
		perMinute: newRateGate(minuteInterval, minuteBurst),
		perHour:   newRateGate(hourInterval, hourBurst),
	}
}

func (l *rateLimiter) Wait(ctx context.Context) error {
	if err := l.perMinute.take(ctx); err != nil {
		return err
	}
	return l.perHour.take(ctx)
}

func (l *rateLimiter) Close() {
	if l.perMinute != nil {
		l.perMinute.stop()
	}
	if l.perHour != nil {
		l.perHour.stop()
	}
}

type rateGate struct {
	tokens chan struct{}
	ticker *time.Ticker
	stopCh chan struct{}
}

func newRateGate(refillInterval time.Duration, burst int) *rateGate {
	if refillInterval <= 0 {
		refillInterval = time.Second
	}
	if burst <= 0 {
		burst = 1
	}

	g := &rateGate{
		tokens: make(chan struct{}, burst),
		ticker: time.NewTicker(refillInterval),
		stopCh: make(chan struct{}),
	}
	for i := 0; i < burst; i++ {
		g.tokens <- struct{}{}
	}

	go func() {
		defer g.ticker.Stop()
		for {
			select {
			case <-g.stopCh:
				return
			case <-g.ticker.C:
				select {
				case g.tokens <- struct{}{}:
				default:
				}
			}
		}
	}()

	return g
}

func (g *rateGate) take(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.tokens:
		return nil
	}
}

func (g *rateGate) stop() {
	select {
	case <-g.stopCh:
		return
	default:
		close(g.stopCh)
	}
}

func newDefaultHTTPClient() *http.Client {
	baseTransport := defaultTransport()
	limitedTransport := &rateLimitedTransport{
		next:    baseTransport,
		limiter: defaultLimiter,
	}
	return &http.Client{
		Timeout: defaultRequestTimeout,
		Transport: &retryingTransport{
			next:        limitedTransport,
			maxAttempts: defaultRetryMaxAttempts,
			baseDelay:   defaultRetryBaseDelay,
			maxDelay:    defaultRetryMaxDelay,
		},
	}
}

func defaultTransport() http.RoundTripper {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := t.Clone()
		clone.MaxIdleConns = defaultMaxIdleConns
		clone.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
		clone.MaxConnsPerHost = defaultMaxConnsPerHost
		clone.IdleConnTimeout = defaultIdleConnTimeout
		clone.ResponseHeaderTimeout = defaultResponseHeaderTimeout
		return clone
	}
	return http.DefaultTransport
}

type APIClient struct {
	BaseURL *url.URL
	HTTP    *http.Client
	token   string

	userAgent string
	headers   http.Header
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

type Option func(*APIClient)

func WithUserAgent(ua string) Option {
	return func(c *APIClient) {
		if trimmed := strings.TrimSpace(ua); trimmed != "" {
			c.userAgent = trimmed
		}
	}
}

func WithAdditionalHeaders(headers http.Header) Option {
	return func(c *APIClient) {
		if headers == nil {
			return
		}
		if c.headers == nil {
			c.headers = make(http.Header)
		}
		for k, vals := range headers {
			for _, v := range vals {
				c.headers.Add(k, v)
			}
		}
	}
}

func NewAPIClient(baseURL, token string, httpClient *http.Client, opts ...Option) (*APIClient, error) {
	if httpClient == nil {
		httpClient = newDefaultHTTPClient()
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}
	apiClient := &APIClient{
		BaseURL:   u,
		HTTP:      httpClient,
		token:     token,
		userAgent: defaultUserAgent,
		headers:   make(http.Header),
	}

	for _, opt := range opts {
		opt(apiClient)
	}

	return apiClient, nil
}

func (c *APIClient) doJSON(ctx context.Context, method, path string, in any, out any, expectedStatus ...int) (*http.Response, error) {
	var body io.ReadCloser
	var bodyBytes []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyBytes = b
		body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	u := c.BaseURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if len(bodyBytes) > 0 {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	for k, vals := range c.headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		msg := string(b)
		if len(b) == maxErrorBodySize {
			msg += "... (truncated)"
		}
		return resp, &APIError{
			StatusCode: resp.StatusCode,
			Message:    msg,
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

type OrganizationLink struct {
	URL  string `json:"url"`
	Icon string `json:"icon"`
	Name string `json:"name"`
}

type OrganizationRead struct {
	ID                        string             `json:"id"`
	OrganizationName          string             `json:"organization_name"`
	SubscriptionPlan          *string            `json:"subscription_plan"`
	HasAdminPanel             bool               `json:"has_admin_panel"`
	CreatedAt                 string             `json:"created_at"`
	UpdatedAt                 string             `json:"updated_at"`
	BillingEmail              *string            `json:"billing_email"`
	OrganizationDisplayName   *string            `json:"organization_display_name"`
	GithubHandle              *string            `json:"github_handle"`
	Location                  *string            `json:"location"`
	Avatar                    *string            `json:"avatar"`
	Links                     []OrganizationLink `json:"links"`
	Description               *string            `json:"description"`
	SpendingCap               *int               `json:"spending_cap"`
	SpendingCapReachedAt      *string            `json:"spending_cap_reached_at"`
	PlanlessGracePeriodEndsAt *string            `json:"planless_grace_period_ends_at"`
	GatewayEnabled            bool               `json:"gateway_enabled"`
	AIEnabled                 bool               `json:"ai_enabled"`
}

type OrganizationCreate struct {
	OrganizationName        string             `json:"organization_name"`
	OrganizationDisplayName *string            `json:"organization_display_name,omitempty"`
	GithubHandle            *string            `json:"github_handle,omitempty"`
	Location                *string            `json:"location,omitempty"`
	Avatar                  *string            `json:"avatar,omitempty"`
	Links                   []OrganizationLink `json:"links,omitempty"`
	Description             *string            `json:"description,omitempty"`
}

type OrganizationUpdate struct {
	OrganizationName        *string             `json:"organization_name,omitempty"`
	BillingEmail            *string             `json:"billing_email,omitempty"`
	OrganizationDisplayName *string             `json:"organization_display_name,omitempty"`
	GithubHandle            *string             `json:"github_handle,omitempty"`
	Location                *string             `json:"location,omitempty"`
	Avatar                  *string             `json:"avatar,omitempty"`
	Links                   *[]OrganizationLink `json:"links,omitempty"`
	Description             *string             `json:"description,omitempty"`
}

func (c *APIClient) organizationsBase() string {
	return "/api/v1/organizations/"
}

func (c *APIClient) organizationPath(id string) string {
	return fmt.Sprintf("%s%s/", c.organizationsBase(), url.PathEscape(id))
}

func (c *APIClient) CreateOrganization(ctx context.Context, in OrganizationCreate) (*OrganizationRead, error) {
	var out OrganizationRead
	_, err := c.doJSON(ctx, http.MethodPost, c.organizationsBase(), in, &out, http.StatusCreated, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) GetOrganization(ctx context.Context, id string) (*OrganizationRead, int, error) {
	var out OrganizationRead
	resp, err := c.doJSON(ctx, http.MethodGet, c.organizationPath(id), nil, &out, http.StatusOK)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

func (c *APIClient) UpdateOrganization(ctx context.Context, id string, in OrganizationUpdate) (*OrganizationRead, error) {
	var out OrganizationRead
	_, err := c.doJSON(ctx, http.MethodPut, c.organizationPath(id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteOrganization(ctx context.Context, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.organizationPath(id), nil, nil, http.StatusNoContent)
	return err
}

func (c *APIClient) ListOrganizations(ctx context.Context) ([]OrganizationRead, error) {
	var out []OrganizationRead
	_, err := c.doJSON(ctx, http.MethodGet, c.organizationsBase(), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return out, nil
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
	return "/api/v1/projects/"
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
	return fmt.Sprintf("/api/v1/projects/%s/alerts/", url.PathEscape(projectID))
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

func (c *APIClient) ListAlerts(ctx context.Context, projectID string) ([]AlertRead, error) {
	var out []AlertRead
	_, err := c.doJSON(ctx, http.MethodGet, c.alertsBase(projectID), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return out, nil
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

// ---- Read Tokens ----

type ReadToken struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	CreatedAt     string  `json:"created_at"`
	ExpiresAt     *string `json:"expires_at"`
	Description   *string `json:"description"`
	Token         *string `json:"token,omitempty"`
	ProjectName   string  `json:"project_name"`
	CreatedByName *string `json:"created_by_name"`
	TokenPrefix   string  `json:"token_prefix"`
}

type CreateReadTokenInput struct {
	ExpiresAt *string `json:"expires_at,omitempty"`
}

func (c *APIClient) readTokensBase(projectID string) string {
	return fmt.Sprintf("/api/v1/projects/%s/read-tokens/", url.PathEscape(projectID))
}
func (c *APIClient) readTokenPath(projectID, tokenID string) string {
	return fmt.Sprintf("%s%s/", c.readTokensBase(projectID), url.PathEscape(tokenID))
}

func (c *APIClient) CreateReadToken(ctx context.Context, projectID string, in CreateReadTokenInput) (*ReadToken, error) {
	var out ReadToken
	_, err := c.doJSON(ctx, http.MethodPost, c.readTokensBase(projectID), in, &out, http.StatusCreated, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) ListReadTokens(ctx context.Context, projectID string) ([]ReadToken, error) {
	var out []ReadToken
	_, err := c.doJSON(ctx, http.MethodGet, c.readTokensBase(projectID), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *APIClient) DeleteReadToken(ctx context.Context, projectID, tokenID string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.readTokenPath(projectID, tokenID), nil, nil, http.StatusNoContent)
	return err
}

// ---- Write Tokens ----

type WriteToken struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	CreatedAt     string  `json:"created_at"`
	ExpiresAt     *string `json:"expires_at"`
	Description   *string `json:"description"`
	Token         *string `json:"token,omitempty"`
	ProjectName   string  `json:"project_name"`
	CreatedByName *string `json:"created_by_name"`
	TokenPrefix   string  `json:"token_prefix"`
}

type CreateWriteTokenInput struct {
	ExpiresAt *string `json:"expires_at,omitempty"`
}

func (c *APIClient) writeTokensBase(projectID string) string {
	return fmt.Sprintf("/api/v1/projects/%s/write-tokens/", url.PathEscape(projectID))
}
func (c *APIClient) writeTokenPath(projectID, tokenID string) string {
	return fmt.Sprintf("%s%s/", c.writeTokensBase(projectID), url.PathEscape(tokenID))
}

func (c *APIClient) CreateWriteToken(ctx context.Context, projectID string, in CreateWriteTokenInput) (*WriteToken, error) {
	var out WriteToken
	_, err := c.doJSON(ctx, http.MethodPost, c.writeTokensBase(projectID), in, &out, http.StatusCreated, http.StatusOK)
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

type DashboardSummary struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     *string `json:"updated_at"`
	CreatedByName *string `json:"created_by_name"`
	UpdatedByName *string `json:"updated_by_name"`
	DashboardName string  `json:"dashboard_name"`
	DashboardSlug string  `json:"dashboard_slug"`
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
	return fmt.Sprintf("/api/v1/projects/%s/dashboards/", url.PathEscape(projectID))
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

func (c *APIClient) ListDashboards(ctx context.Context, projectID string) ([]DashboardSummary, error) {
	var out []DashboardSummary
	_, err := c.doJSON(ctx, http.MethodGet, c.dashboardsBase(projectID), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return out, nil
}
