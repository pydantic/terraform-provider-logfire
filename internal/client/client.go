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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

const (
	defaultRequestTimeout        = 90 * time.Second
	defaultRequestsPerMinute     = 50
	defaultRequestsPerHour       = 1000
	defaultRetryMaxAttempts      = 5
	maxRateLimitRetryWait        = defaultRequestTimeout
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
	// Logfire limits API requests per organization on each backend. Every
	// client in the process shares one limiter per host, so requests to one
	// backend never spend another's.
	hostLimiters   = map[string]*rateLimiter{}
	hostLimitersMu sync.Mutex
)

type rateLimitedTransport struct {
	next http.RoundTripper
}

func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Host)
	hostLimitersMu.Lock()
	limiter := hostLimiters[host]
	if limiter == nil {
		limiter = newRateLimiter(defaultRequestsPerMinute, defaultRequestsPerHour)
		hostLimiters[host] = limiter
	}
	hostLimitersMu.Unlock()
	if err := limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

type retryingTransport struct {
	next        http.RoundTripper
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

type disableAutomaticRetriesContextKey struct{}

func disableAutomaticRetries(ctx context.Context) context.Context {
	return context.WithValue(ctx, disableAutomaticRetriesContextKey{}, true)
}

func (t *retryingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	attempts := t.maxAttempts
	if ctx.Value(disableAutomaticRetriesContextKey{}) == true {
		attempts = 1
	}

	start := time.Now()
	for attempt, sent := 0, 0; ; sent++ {
		if sent > 0 {
			switch {
			case req.GetBody != nil:
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				req.Body = body
			case req.Body != nil:
				return nil, errors.New("cannot retry request with body: GetBody is nil")
			}
		}

		resp, err := t.next.RoundTrip(req)

		// The Logfire public API rate limit refuses a request before the route
		// runs, so the request had no effect. A retry is then safe for every
		// method, a create included. Other 429 responses do not carry this
		// header, and the provider cannot tell whether those requests ran.
		refused := err == nil && resp.StatusCode == http.StatusTooManyRequests &&
			resp.Header.Get("X-RateLimit-Resource") == "public_api"

		var delay time.Duration
		if refused {
			delay = max(retryDelay(resp, t.baseDelay, t.maxDelay, 0), t.baseDelay)
			// Add up to a quarter of delay, so concurrent requests that got the
			// same Retry-After do not retry together.
			if spread := int64(delay / 4); spread > 0 {
				delay += time.Duration(rand.Int63n(spread))
			}
			if !retryFitsBudget(ctx, start, delay) {
				return resp, nil
			}
		} else {
			var retry bool
			switch {
			case attempt >= attempts-1:
			case err != nil:
				var netErr net.Error
				var opErr *net.OpError
				retry = !errors.Is(err, context.Canceled) &&
					!errors.Is(err, context.DeadlineExceeded) &&
					ctx.Err() == nil &&
					(errors.As(err, &netErr) && netErr.Timeout() ||
						errors.As(err, &opErr))
			default:
				retry = resp.StatusCode == http.StatusTooManyRequests ||
					resp.StatusCode == http.StatusRequestTimeout ||
					resp.StatusCode >= 500 && resp.StatusCode < 600
			}
			if !retry {
				return resp, err
			}
			delay = retryDelay(resp, t.baseDelay, t.maxDelay, attempt)
			attempt++
		}

		if err == nil {
			drainResponse(resp)
		}
		if delay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
}

// retryFitsBudget reports whether a wait of delay ends before the request
// deadline and inside maxRateLimitRetryWait from the first attempt. The second
// bound applies to a client without a timeout.
func retryFitsBudget(ctx context.Context, start time.Time, delay time.Duration) bool {
	end := time.Now().Add(delay)
	if deadline, ok := ctx.Deadline(); ok && !end.Before(deadline) {
		return false
	}
	return end.Sub(start) <= maxRateLimitRetryWait
}

func drainResponse(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func retryDelay(resp *http.Response, base, maxDelay time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = defaultRetryMaxDelay
	}

	if resp != nil {
		ra := resp.Header.Get("Retry-After")
		if seconds, err := strconv.Atoi(ra); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if t, err := http.ParseTime(ra); err == nil {
			if delay := time.Until(t); delay > 0 {
				return delay
			}
		}
	}

	delay := min(base*time.Duration(1<<attempt), maxDelay)
	// Add jitter to avoid thundering herd on retries.
	return delay/2 + time.Duration(rand.Int63n(int64(delay/2)))
}

// rateLimiter holds a per-minute and a per-hour token bucket. Both start full.
// They refill on demand, so a limiter starts no goroutine and needs no cleanup.
type rateLimiter struct {
	perMinute *rate.Limiter
	perHour   *rate.Limiter
}

func newRateLimiter(requestsPerMinute, requestsPerHour int) *rateLimiter {
	bucket := func(window time.Duration, n int) *rate.Limiter {
		return rate.NewLimiter(rate.Every(window/time.Duration(n)), n)
	}
	return &rateLimiter{
		perMinute: bucket(time.Minute, requestsPerMinute),
		perHour:   bucket(time.Hour, requestsPerHour),
	}
}

func (l *rateLimiter) Wait(ctx context.Context) error {
	if err := l.perMinute.Wait(ctx); err != nil {
		return err
	}
	return l.perHour.Wait(ctx)
}

type APIClient struct {
	BaseURL *url.URL
	HTTP    *http.Client
	token   string

	userAgent        string
	headers          http.Header
	dashboardManager string

	// organization is the organization that ForOrganization selected. Empty
	// means the organization of the API key.
	organization string

	shared *clientShared
}

// clientShared is the state that a client and the clients derived from it with
// ForOrganization share.
type clientShared struct {
	tokenMu sync.Mutex
	tokens  map[string]orgTokenEntry
	// exchanges lets concurrent requests for one organization share one
	// exchange.
	exchanges singleflight.Group

	// legacySchedules is set once the instance has answered on the deprecated
	// /schedules/ route, so later calls skip the notification windows route.
	legacySchedules atomic.Bool
}

// ForOrganization returns a client that acts in the named organization. Its
// requests carry a short-lived token that the instance-admin API key exchanges
// for that organization, except on instance routes, which keep the API key. An
// empty name returns a client that acts in the organization of the API key.
func (c *APIClient) ForOrganization(name string) *APIClient {
	derived := *c
	derived.organization = name
	return &derived
}

func (c *APIClient) Organization() string {
	return c.organization
}

type APIError struct {
	StatusCode int
	Message    string
	// BackendVersion is the release tag reported by the instance's
	// Logfire-Version response header, when the server provides one that is a
	// release (vYYYY-MM-DD.NN). Builds with no release tag report an image
	// identity instead, which carries no version meaning and is left empty.
	BackendVersion string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("API error: status %d", e.StatusCode)
}

// releaseVersionPattern matches the platform's release tags, e.g.
// v2026-09-14.01. The Logfire-Version response header carries one when the
// deployment sets it. Builds with no release tag answer with an image identity
// instead, which is not a version clients can compare, so it counts as unknown.
var releaseVersionPattern = regexp.MustCompile(`^v\d{4}-\d{2}-\d{2}\.\d+$`)

type Option func(*APIClient)

// WithDashboardManager names the tool recorded as the manager of the
// dashboards this client writes, such as "Pulumi" or "Crossplane". The
// default is Terraform.
func WithDashboardManager(tool string) Option {
	return func(c *APIClient) {
		if trimmed := strings.TrimSpace(tool); trimmed != "" {
			c.dashboardManager = trimmed
		}
	}
}

func WithUserAgent(ua string) Option {
	return func(c *APIClient) {
		if trimmed := strings.TrimSpace(ua); trimmed != "" {
			c.userAgent = trimmed
		}
	}
}

func WithAdditionalHeaders(headers http.Header) Option {
	return func(c *APIClient) {
		for k, vals := range headers {
			for _, v := range vals {
				c.headers.Set(k, v)
			}
		}
	}
}

func NewAPIClient(baseURL, token string, httpClient *http.Client, opts ...Option) (*APIClient, error) {
	if httpClient == nil {
		transport := http.DefaultTransport
		if t, ok := transport.(*http.Transport); ok {
			clone := t.Clone()
			clone.MaxIdleConns = defaultMaxIdleConns
			clone.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
			clone.MaxConnsPerHost = defaultMaxConnsPerHost
			clone.IdleConnTimeout = defaultIdleConnTimeout
			clone.ResponseHeaderTimeout = defaultResponseHeaderTimeout
			transport = clone
		}
		httpClient = &http.Client{
			Timeout: defaultRequestTimeout,
			Transport: &retryingTransport{
				next:        &rateLimitedTransport{next: transport},
				maxAttempts: defaultRetryMaxAttempts,
				baseDelay:   defaultRetryBaseDelay,
				maxDelay:    defaultRetryMaxDelay,
			},
		}
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}
	apiClient := &APIClient{
		BaseURL:          u,
		HTTP:             httpClient,
		token:            token,
		userAgent:        defaultUserAgent,
		headers:          make(http.Header),
		shared:           &clientShared{tokens: make(map[string]orgTokenEntry)},
		dashboardManager: "terraform",
	}
	for _, opt := range opts {
		opt(apiClient)
	}
	return apiClient, nil
}

// instanceRoutePrefix starts the routes that authorize the instance-admin API
// key itself. The other routes resolve the organization from the token.
const instanceRoutePrefix = "/api/v1/instance/"

func apiPath(segments ...string) string {
	path := "/api/v1"
	for _, segment := range segments {
		path += "/" + url.PathEscape(segment)
	}
	return path + "/"
}

func (c *APIClient) doJSON(ctx context.Context, method, path string, in, out any, expectedStatus ...int) (*http.Response, error) {
	if c.organization == "" || strings.HasPrefix(path, instanceRoutePrefix) {
		return c.doJSONAuthorized(ctx, c.token, method, path, in, out, expectedStatus...)
	}
	return c.withOrganizationContext(ctx, c.organization, method, path, in, out, expectedStatus...)
}

// doJSONAuthorized is doJSON with an explicit bearer token, for calls made with
// a short-lived exchanged credential instead of the configured provider token.
func (c *APIClient) doJSONAuthorized(ctx context.Context, bearer, method, path string, in, out any, expectedStatus ...int) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, nil)
	if err != nil {
		return nil, err
	}
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(payload))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.send(req, expectedStatus...)
	if err != nil {
		return resp, err
	}
	defer drainResponse(resp)

	// A success with no body would leave out empty, and a list would then look
	// like an empty collection.
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return resp, nil
	}
	switch err := json.NewDecoder(resp.Body).Decode(out); {
	case errors.Is(err, io.EOF) && resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp, fmt.Errorf("decode response: the server answered %d with an empty body", resp.StatusCode)
	case err != nil && !errors.Is(err, io.EOF):
		return resp, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}

func (c *APIClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	reference, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("parse request path: %w", err)
	}
	if strings.Contains(reference.Path, "//") {
		return nil, fmt.Errorf("%s %s: the resource ID is missing. Import the resource, or remove it from state if it cannot be imported", method, reference.Path)
	}
	u := c.BaseURL.ResolveReference(reference)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	return req, nil
}

// send adds the client's headers to req and runs it. The caller drains the
// response of a success. An unexpected status comes back with its response,
// already drained, as an *APIError.
func (c *APIClient) send(req *http.Request, expectedStatus ...int) (*http.Response, error) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	for k, vals := range c.headers {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	if slices.Contains(expectedStatus, resp.StatusCode) {
		return resp, nil
	}

	defer drainResponse(resp)
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	msg := string(b)
	if len(b) == maxErrorBodySize {
		msg += "... (truncated)"
	}
	version := strings.TrimSpace(resp.Header.Get("Logfire-Version"))
	if !releaseVersionPattern.MatchString(version) {
		version = ""
	}
	return resp, &APIError{
		StatusCode:     resp.StatusCode,
		Message:        msg,
		BackendVersion: version,
	}
}

func request[T any](ctx context.Context, c *APIClient, method, path string, in any) (*T, error) {
	out := new(T)
	if _, err := c.doJSON(ctx, method, path, in, out, http.StatusOK); err != nil {
		return nil, err
	}
	return out, nil
}

// create sends a POST that is not idempotent, so a retry could create a second
// resource.
func create[T any](ctx context.Context, c *APIClient, path string, in any, expectedStatus ...int) (*T, error) {
	out := new(T)
	ctx = disableAutomaticRetries(ctx)
	if _, err := c.doJSON(ctx, http.MethodPost, path, in, out, expectedStatus...); err != nil {
		return nil, err
	}
	return out, nil
}

func list[T any](ctx context.Context, c *APIClient, path string) ([]T, error) {
	out, err := request[[]T](ctx, c, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// getWithStatus reads a resource and reports the HTTP status, which is 0 when
// no response arrived, so that callers can tell a deleted resource from a
// failure.
func getWithStatus[T any](ctx context.Context, c *APIClient, path string) (*T, int, error) {
	out := new(T)
	resp, err := c.doJSON(ctx, http.MethodGet, path, nil, out, http.StatusOK)
	return withStatus(out, resp, err)
}

func withStatus[T any](out *T, resp *http.Response, err error) (*T, int, error) {
	if err == nil {
		return out, http.StatusOK, nil
	}
	if resp == nil {
		return nil, 0, err
	}
	return nil, resp.StatusCode, err
}

func (c *APIClient) remove(ctx context.Context, path string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, path, nil, nil, http.StatusNoContent)
	return err
}

type cursorPage[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

// pagePath returns the route of the page after cursor, which is empty for the
// first page.
func pagePath(base, cursor string) string {
	query := url.Values{"limit": []string{"100"}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	return base + "?" + query.Encode()
}

func listPages[T any](ctx context.Context, c *APIClient, base string) ([]T, error) {
	var all []T
	cursor := ""
	for {
		var page cursorPage[T]
		_, err := c.doJSON(ctx, http.MethodGet, pagePath(base, cursor), nil, &page, http.StatusOK)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.NextCursor == nil {
			return all, nil
		}
		cursor = *page.NextCursor
	}
}

// NullableField helps send explicit nulls (to clear values) while still
// allowing us to omit fields for partial updates.
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
	OrganizationDisplayName *string             `json:"organization_display_name,omitempty"`
	GithubHandle            *string             `json:"github_handle,omitempty"`
	Location                *string             `json:"location,omitempty"`
	Avatar                  *string             `json:"avatar,omitempty"`
	Links                   *[]OrganizationLink `json:"links,omitempty"`
	Description             *string             `json:"description,omitempty"`
}

// instanceOrganizationsPath is the canonical home of the org create and list
// operations. The legacy /organizations/ variants are deprecated.
const instanceOrganizationsPath = "/api/v1/instance/organizations/"

func (c *APIClient) CreateOrganization(ctx context.Context, in OrganizationCreate) (*OrganizationRead, error) {
	return create[OrganizationRead](
		ctx, c, instanceOrganizationsPath, in, http.StatusCreated, http.StatusOK)
}

func (c *APIClient) ListOrganizations(ctx context.Context) ([]OrganizationRead, error) {
	out, err := list[OrganizationRead](ctx, c, instanceOrganizationsPath)
	// On the instance organization collection a 404 can only mean that the
	// route is missing: it has no resource-id path segment and no 404 handler
	// branch.
	return out, unavailableIfNotFound(
		err, http.MethodGet, instanceOrganizationsPath, "v2026-06-03.01", "")
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
	Description NullableField[string] `json:"description,omitzero"`
	Visibility  NullableField[string] `json:"visibility,omitzero"`
}

func (c *APIClient) CreateProject(ctx context.Context, in ProjectCreate) (*ProjectRead, error) {
	return create[ProjectRead](ctx, c, apiPath("projects"), in, http.StatusCreated, http.StatusOK)
}

func (c *APIClient) GetProject(ctx context.Context, id string) (*ProjectRead, int, error) {
	return getWithStatus[ProjectRead](ctx, c, apiPath("projects", id))
}

func (c *APIClient) UpdateProject(ctx context.Context, id string, in ProjectUpdate) (*ProjectRead, error) {
	return request[ProjectRead](ctx, c, http.MethodPut, apiPath("projects", id), in)
}

func (c *APIClient) DeleteProject(ctx context.Context, id string) error {
	return c.remove(ctx, apiPath("projects", id))
}

func (c *APIClient) ListProjects(ctx context.Context) ([]ProjectRead, error) {
	return list[ProjectRead](ctx, c, apiPath("projects"))
}

type AlertRead struct {
	ID             string             `json:"id"`
	OrganizationID string             `json:"organization_id"`
	ProjectID      string             `json:"project_id"`
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      *string            `json:"updated_at"`
	CreatedByName  *string            `json:"created_by_name"`
	UpdatedByName  *string            `json:"updated_by_name"`
	Name           string             `json:"name"`
	Description    *string            `json:"description"`
	Query          string             `json:"query"`
	TimeWindow     string             `json:"time_window"`
	Frequency      string             `json:"frequency"`
	Watermark      string             `json:"watermark"`
	Environments   []string           `json:"environments"`
	Channels       []AlertChannelRead `json:"channels"`
	// ChannelAssignments is nil on a Logfire release that does not return
	// it. A present empty list decodes to an empty, non-nil slice.
	ChannelAssignments []ChannelAssignment `json:"channel_assignments"`
	NotifyWhen         string              `json:"notify_when"`
	Active             bool                `json:"active"`
	LastRun            *string             `json:"last_run,omitempty"`
	HasMatches         *bool               `json:"has_matches,omitempty"`
	HasErrors          *bool               `json:"has_errors,omitempty"`
	Result             json.RawMessage     `json:"result,omitempty"`
	ResultLength       *int                `json:"result_length,omitempty"`
	// InstancePolicy is nil when the alert evaluates its whole result as one,
	// and on a Logfire release that predates instance policies.
	InstancePolicy *AlertInstancePolicy `json:"instance_policy"`
}

// AlertInstancePolicy makes an alert track each result row as its own
// occurrence, keyed by the identity columns. The API requires ContinuityRuns
// but deprecates it and ignores it when it settles occurrences.
type AlertInstancePolicy struct {
	IdentityColumns []string `json:"identity_columns"`
	PendingRuns     int64    `json:"pending_runs"`
	MissingRuns     int64    `json:"missing_runs"`
	MissingBehavior string   `json:"missing_behavior"`
	ContinuityRuns  int64    `json:"continuity_runs"`
}

// ChannelAssignment is the API's `AlertChannelAssignment`: one channel an
// alert notifies, optionally limited to the windows of a delivery schedule.
// Normal alerts and SLO burn-rate alerts use the same type.
type ChannelAssignment struct {
	ChannelID  string  `json:"channel_id"`
	ScheduleID *string `json:"schedule_id,omitempty"`
}

type AlertChannelRead struct {
	ChannelRead
	ScheduleID *string `json:"schedule_id"`
}

type AlertCreate struct {
	Name               string               `json:"name"`
	Description        *string              `json:"description"`
	Active             *bool                `json:"active,omitempty"`
	Query              string               `json:"query"`
	TimeWindow         string               `json:"time_window"`
	Frequency          string               `json:"frequency"`
	Watermark          string               `json:"watermark"`
	Environments       []string             `json:"environments,omitempty"`
	ChannelAssignments []ChannelAssignment  `json:"channel_assignments"`
	NotifyWhen         string               `json:"notify_when"`
	InstancePolicy     *AlertInstancePolicy `json:"instance_policy,omitempty"`
}

type AlertUpdate struct {
	Name               *string              `json:"name,omitempty"`
	Description        *string              `json:"description,omitempty"`
	TimeWindow         *string              `json:"time_window,omitempty"`
	Frequency          *string              `json:"frequency,omitempty"`
	Watermark          *string              `json:"watermark,omitempty"`
	Active             *bool                `json:"active,omitempty"`
	Query              *string              `json:"query,omitempty"`
	Environments       *[]string            `json:"environments,omitempty"`
	ChannelAssignments *[]ChannelAssignment `json:"channel_assignments,omitempty"`
	NotifyWhen         *string              `json:"notify_when,omitempty"`
	// InstancePolicy replaces the policy when set. A set pointer to nil sends
	// null, which removes it, and an unset field keeps the current policy.
	InstancePolicy **AlertInstancePolicy `json:"instance_policy,omitempty"`
}

func (c *APIClient) CreateAlert(ctx context.Context, projectID string, in AlertCreate) (*AlertRead, error) {
	path := apiPath("projects", projectID, "alerts")
	return create[AlertRead](ctx, c, path, in, http.StatusCreated)
}

func (c *APIClient) GetAlert(ctx context.Context, projectID, id string) (*AlertRead, int, error) {
	return getWithStatus[AlertRead](ctx, c, apiPath("projects", projectID, "alerts", id))
}

func (c *APIClient) UpdateAlert(ctx context.Context, projectID, id string, in AlertUpdate) (*AlertRead, error) {
	path := apiPath("projects", projectID, "alerts", id)
	return request[AlertRead](ctx, c, http.MethodPut, path, in)
}

func (c *APIClient) DeleteAlert(ctx context.Context, projectID, id string) error {
	return c.remove(ctx, apiPath("projects", projectID, "alerts", id))
}

func (c *APIClient) ListAlerts(ctx context.Context, projectID string) ([]AlertRead, error) {
	return list[AlertRead](ctx, c, apiPath("projects", projectID, "alerts"))
}

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
	Recipients []string `json:"recipients"`
}

type OpsgenieConfig struct {
	ChannelConfigBase
	AuthKey *string `json:"auth_key,omitempty"`
	BaseURL *string `json:"base_url,omitempty"`
}

type PagerdutyConfig struct {
	ChannelConfigBase
	RoutingKey *string `json:"routing_key,omitempty"`
	Region     *string `json:"region,omitempty"`
}

type SlackIntegrationConfig struct {
	ChannelConfigBase
	InstallID          *string `json:"install_id,omitempty"`
	ChannelID          *string `json:"channel_id,omitempty"`
	IncludeAgentPrompt *bool   `json:"include_agent_prompt,omitempty"`
}

// PagerdutyIntegrationConfig pages through the organization's installed Logfire
// PagerDuty App. Unlike PagerdutyConfig it holds no credential: the backend
// resolves the approved service's integration key from the installation at send
// time.
type PagerdutyIntegrationConfig struct {
	ChannelConfigBase
	InstallID *string `json:"install_id,omitempty"`
	ServiceID *string `json:"service_id,omitempty"`
}

type ChannelConfig struct {
	Type               string   `json:"type"`
	Format             *string  `json:"format,omitempty"`
	URL                *string  `json:"url,omitempty"`
	Recipients         []string `json:"recipients,omitempty"`
	AuthKey            *string  `json:"auth_key,omitempty"`
	BaseURL            *string  `json:"base_url,omitempty"`
	RoutingKey         *string  `json:"routing_key,omitempty"`
	Region             *string  `json:"region,omitempty"`
	InstallID          *string  `json:"install_id,omitempty"`
	ChannelID          *string  `json:"channel_id,omitempty"`
	ServiceID          *string  `json:"service_id,omitempty"`
	IncludeAgentPrompt *bool    `json:"include_agent_prompt,omitempty"`
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

// ChannelCreate.Config and ChannelUpdate.Config hold a WebhookConfig,
// EmailConfig, OpsgenieConfig, PagerdutyConfig, PagerdutyIntegrationConfig, or
// SlackIntegrationConfig.
type ChannelCreate struct {
	Label  string      `json:"label"`
	Config interface{} `json:"config"`
}

type ChannelUpdate struct {
	Label  NullableField[string] `json:"label,omitzero"`
	Config *interface{}          `json:"config,omitempty"`
	Active NullableField[bool]   `json:"active,omitzero"`
}

func (c *APIClient) CreateChannel(ctx context.Context, in ChannelCreate) (*ChannelRead, error) {
	return create[ChannelRead](ctx, c, apiPath("channels"), in, http.StatusCreated)
}

func (c *APIClient) ListChannels(ctx context.Context) ([]ChannelRead, error) {
	return list[ChannelRead](ctx, c, apiPath("channels"))
}

func (c *APIClient) GetChannel(ctx context.Context, id string) (*ChannelRead, int, error) {
	return getWithStatus[ChannelRead](ctx, c, apiPath("channels", id))
}

func (c *APIClient) UpdateChannel(ctx context.Context, id string, in ChannelUpdate) (*ChannelRead, error) {
	return request[ChannelRead](ctx, c, http.MethodPut, apiPath("channels", id), in)
}

func (c *APIClient) DeleteChannel(ctx context.Context, id string) error {
	return c.remove(ctx, apiPath("channels", id))
}

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

func (c *APIClient) CreateReadToken(ctx context.Context, projectID string, in CreateReadTokenInput) (*ReadToken, error) {
	path := apiPath("projects", projectID, "read-tokens")
	return create[ReadToken](ctx, c, path, in, http.StatusCreated, http.StatusOK)
}

func (c *APIClient) ListReadTokens(ctx context.Context, projectID string) ([]ReadToken, error) {
	return list[ReadToken](ctx, c, apiPath("projects", projectID, "read-tokens"))
}

func (c *APIClient) DeleteReadToken(ctx context.Context, projectID, tokenID string) error {
	return c.remove(ctx, apiPath("projects", projectID, "read-tokens", tokenID))
}

type WriteToken ReadToken

type CreateWriteTokenInput CreateReadTokenInput

func (c *APIClient) CreateWriteToken(ctx context.Context, projectID string, in CreateWriteTokenInput) (*WriteToken, error) {
	path := apiPath("projects", projectID, "write-tokens")
	return create[WriteToken](ctx, c, path, in, http.StatusCreated, http.StatusOK)
}

func (c *APIClient) ListWriteTokens(ctx context.Context, projectID string) ([]WriteToken, error) {
	return list[WriteToken](ctx, c, apiPath("projects", projectID, "write-tokens"))
}

func (c *APIClient) DeleteWriteToken(ctx context.Context, projectID, tokenID string) error {
	return c.remove(ctx, apiPath("projects", projectID, "write-tokens", tokenID))
}

type FrontendApplication struct {
	ID               string  `json:"id"`
	ProjectID        string  `json:"project_id"`
	Name             string  `json:"name"`
	ServiceNamespace *string `json:"service_namespace"`
	Environment      *string `json:"environment"`
	CreatedAt        string  `json:"created_at"`
}

type FrontendApplicationCreate struct {
	Name                     string  `json:"name"`
	ServiceNamespace         *string `json:"service_namespace,omitempty"`
	Environment              *string `json:"environment,omitempty"`
	AdoptExistingServiceName bool    `json:"adopt_existing_service_name,omitempty"`
}

type FrontendApplicationCreated struct {
	FrontendApplication
	TokenID string `json:"token_id"`
	Token   string `json:"token"`
}

type FrontendApplicationToken struct {
	ID          string  `json:"id"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	Token       *string `json:"token"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   *string `json:"expires_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

type FrontendApplicationTokenCreated struct {
	TokenID     string  `json:"token_id"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	Token       string  `json:"token"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   *string `json:"expires_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

func (c *APIClient) CreateFrontendApplication(ctx context.Context, projectID string, in FrontendApplicationCreate) (*FrontendApplicationCreated, error) {
	path := apiPath("projects", projectID, "frontend-applications")
	return create[FrontendApplicationCreated](ctx, c, path, in, http.StatusCreated)
}

func (c *APIClient) GetFrontendApplication(ctx context.Context, projectID, applicationID string) (*FrontendApplication, int, error) {
	var out FrontendApplication
	path := apiPath("projects", projectID, "frontend-applications", applicationID)
	resp, err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, resp.StatusCode, nil
	}
	return &out, resp.StatusCode, nil
}

func (c *APIClient) ListFrontendApplications(ctx context.Context, projectID string) ([]FrontendApplication, error) {
	path := apiPath("projects", projectID, "frontend-applications")
	return listPages[FrontendApplication](ctx, c, path)
}

func (c *APIClient) DeleteFrontendApplication(ctx context.Context, projectID, applicationID string) error {
	return c.remove(ctx, apiPath("projects", projectID, "frontend-applications", applicationID))
}

func (c *APIClient) ListFrontendApplicationTokens(ctx context.Context, projectID, applicationID string) ([]FrontendApplicationToken, error) {
	path := apiPath("projects", projectID, "frontend-applications", applicationID, "tokens")
	return listPages[FrontendApplicationToken](ctx, c, path)
}

func (c *APIClient) CreateFrontendApplicationToken(ctx context.Context, projectID, applicationID string) (*FrontendApplicationTokenCreated, error) {
	path := apiPath("projects", projectID, "frontend-applications", applicationID, "tokens")
	return create[FrontendApplicationTokenCreated](ctx, c, path, nil, http.StatusCreated)
}

func (c *APIClient) RevokeFrontendApplicationToken(ctx context.Context, projectID, applicationID, tokenID string) error {
	path := apiPath(
		"projects", projectID, "frontend-applications", applicationID, "tokens", tokenID, "revoke")
	_, err := c.doJSON(ctx, http.MethodPost, path, nil, nil, http.StatusNoContent)
	return err
}

type DashboardCreateRequest struct {
	Name       string          `json:"name"`
	Slug       string          `json:"slug"`
	Definition json.RawMessage `json:"definition"`
	DashboardManager
}

type DashboardUpdateRequest struct {
	Name          *string          `json:"name,omitempty"`
	Definition    *json.RawMessage `json:"definition,omitempty"`
	ReplaceLayout bool             `json:"-"`
	DashboardManager
}

// DashboardManager marks a dashboard as managed by the tool that writes it.
// Logfire shows the tool on the dashboard and, unless ManagedAllowEdits is
// set, blocks edits from the UI and the MCP server. The dashboards API stays
// open to the manager. Backends without managed dashboards ignore these fields.
type DashboardManager struct {
	ManagedBy         string  `json:"managed_by,omitempty"`
	ManagedByName     *string `json:"managed_by_name,omitempty"`
	ManagedAllowEdits bool    `json:"managed_allow_edits,omitempty"`
}

func (c *APIClient) DashboardManager(allowEdits bool) DashboardManager {
	name := c.dashboardManager
	m := DashboardManager{
		ManagedBy:         "other",
		ManagedByName:     &name,
		ManagedAllowEdits: allowEdits,
	}
	tool := strings.ToLower(c.dashboardManager)
	if tool == "terraform" || tool == "pulumi" {
		m.ManagedBy, m.ManagedByName = tool, nil
	}
	return m
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
	// Backends without managed dashboards leave these out.
	ManagedBy         *string `json:"managed_by"`
	ManagedAllowEdits *bool   `json:"managed_allow_edits"`
}

type GetDashboardResponse struct {
	Dashboard json.RawMessage `json:"dashboard"`
}

func (c *APIClient) CreateDashboard(ctx context.Context, projectID string, in DashboardCreateRequest) (*Dashboard, error) {
	path := apiPath("projects", projectID, "dashboards")
	return create[Dashboard](ctx, c, path, in, http.StatusOK, http.StatusCreated)
}

func (c *APIClient) GetDashboard(ctx context.Context, projectID, dashboardID string) (*GetDashboardResponse, int, error) {
	path := apiPath("projects", projectID, "dashboards", dashboardID)
	return getWithStatus[GetDashboardResponse](ctx, c, path)
}

func (c *APIClient) UpdateDashboard(ctx context.Context, projectID, dashboardID string, in DashboardUpdateRequest) (*Dashboard, error) {
	path := apiPath("projects", projectID, "dashboards", dashboardID)
	if in.ReplaceLayout {
		path += "?replace_layout=true"
	}
	return request[Dashboard](ctx, c, http.MethodPut, path, in)
}

func (c *APIClient) DeleteDashboard(ctx context.Context, projectID, dashboardID string) error {
	return c.remove(ctx, apiPath("projects", projectID, "dashboards", dashboardID))
}

func (c *APIClient) ListDashboards(ctx context.Context, projectID string) ([]DashboardSummary, error) {
	return list[DashboardSummary](ctx, c, apiPath("projects", projectID, "dashboards"))
}

// The SLO endpoints are experimental and not yet part of the published Logfire
// OpenAPI schema; their shape may change.

type SloRead struct {
	ID                     string   `json:"id"`
	ProjectID              string   `json:"project_id"`
	ScopeKind              string   `json:"scope_kind"`
	ScopeValue             string   `json:"scope_value"`
	Name                   string   `json:"name"`
	Description            *string  `json:"description"`
	Source                 string   `json:"source"`
	MetricAggregation      string   `json:"metric_aggregation"`
	TotalQuery             string   `json:"total_query"`
	BadQuery               string   `json:"bad_query"`
	Threshold              *string  `json:"threshold"`
	Comparison             *string  `json:"comparison"`
	TargetPercent          string   `json:"target_percent"`
	RollingWindow          string   `json:"rolling_window"`
	Environments           []string `json:"environments"`
	CreatedAt              string   `json:"created_at"`
	UpdatedAt              *string  `json:"updated_at"`
	CreatedByName          *string  `json:"created_by_name"`
	UpdatedByName          *string  `json:"updated_by_name"`
	CurrentSliPercent      *string  `json:"current_sli_percent"`
	BudgetRemainingPercent *string  `json:"budget_remaining_percent"`
	LastCheckedAt          *string  `json:"last_checked_at"`
	PredicateVersion       int      `json:"predicate_version"`
	// Alerts is the SLO's burn-rate alerts keyed by tier, with each alert's
	// channels as stored on the alert. It is nil on a Logfire release that
	// does not return it.
	Alerts *SloTierAlerts `json:"alerts,omitempty"`
}

type SloTierAlerts struct {
	Fast   SloTierAlert `json:"fast"`
	Medium SloTierAlert `json:"medium"`
	Slow   SloTierAlert `json:"slow"`
}

func (a *SloTierAlerts) Tier(name string) SloTierAlert {
	switch name {
	case "fast":
		return a.Fast
	case "medium":
		return a.Medium
	default:
		return a.Slow
	}
}

type SloTierAlert struct {
	// AlertID is nil only for an SLO created before every SLO kept all three
	// tier alerts; the SLO's next update creates the alert.
	AlertID *string `json:"alert_id"`
	// Severity is "page" for the fast and medium tiers, "ticket" for slow.
	Severity string `json:"severity"`
	// Viable is false when the tier cannot fire at the SLO's target. The
	// alert keeps its channels but is not evaluated.
	Viable             bool                `json:"viable"`
	ChannelAssignments []ChannelAssignment `json:"channel_assignments"`
}

type SloCreate struct {
	ScopeKind         string  `json:"scope_kind"`
	ScopeValue        string  `json:"scope_value"`
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	Source            *string `json:"source,omitempty"`
	MetricAggregation *string `json:"metric_aggregation,omitempty"`
	TotalQuery        string  `json:"total_query"`
	// BadQuery is required for every mode except `histogram_threshold`, which
	// uses Threshold/Comparison instead and omits it (the API rejects an empty
	// string, so it must be absent, not "").
	BadQuery             *string  `json:"bad_query,omitempty"`
	Threshold            *string  `json:"threshold,omitempty"`
	Comparison           *string  `json:"comparison,omitempty"`
	TargetPercent        string   `json:"target_percent"`
	RollingWindowSeconds int64    `json:"rolling_window_seconds"`
	Environments         []string `json:"environments,omitempty"`
	// Alerts sets the channels of the named tier alerts. A tier that is left
	// out starts with no channels. The create-only `page_channel_ids` /
	// `ticket_channel_ids` shorthand is not used.
	Alerts *SloAlertsDelivery `json:"alerts,omitempty"`
}

type SloUpdate struct {
	Name              *string `json:"name,omitempty"`
	Description       *string `json:"description,omitempty"`
	Source            *string `json:"source,omitempty"`
	MetricAggregation *string `json:"metric_aggregation,omitempty"`
	TotalQuery        *string `json:"total_query,omitempty"`
	BadQuery          *string `json:"bad_query,omitempty"`
	// Threshold and Comparison are tri-state on update: absent (`**string` nil)
	// omits the field, a `*string` nil sends explicit JSON null (to clear them
	// when switching a histogram SLO to another mode), and a set value patches
	// them. The API's merge keeps the existing value on omission, so clearing
	// requires the explicit null.
	Threshold            **string  `json:"threshold,omitempty"`
	Comparison           **string  `json:"comparison,omitempty"`
	TargetPercent        *string   `json:"target_percent,omitempty"`
	RollingWindowSeconds *int64    `json:"rolling_window_seconds,omitempty"`
	Environments         *[]string `json:"environments,omitempty"`
	// Alerts replaces the channels of each named tier alert. A tier that is
	// left out, or an update without Alerts, keeps its channels.
	Alerts *SloAlertsDelivery `json:"alerts,omitempty"`
}

// SloAlertsDelivery is the request's delivery per burn-rate tier alert. The
// API rejects an unknown tier key.
type SloAlertsDelivery struct {
	Fast   *SloTierDelivery `json:"fast,omitempty"`
	Medium *SloTierDelivery `json:"medium,omitempty"`
	Slow   *SloTierDelivery `json:"slow,omitempty"`
}

func (d *SloAlertsDelivery) Tier(name string) **SloTierDelivery {
	switch name {
	case "fast":
		return &d.Fast
	case "medium":
		return &d.Medium
	default:
		return &d.Slow
	}
}

type SloTierDelivery struct {
	ChannelAssignments []ChannelAssignment `json:"channel_assignments"`
}

func (c *APIClient) CreateSlo(ctx context.Context, projectID string, in SloCreate) (*SloRead, error) {
	return create[SloRead](ctx, c, apiPath("projects", projectID, "slos"), in, http.StatusCreated)
}

func (c *APIClient) GetSlo(ctx context.Context, projectID, id string) (*SloRead, int, error) {
	return getWithStatus[SloRead](ctx, c, apiPath("projects", projectID, "slos", id))
}

func (c *APIClient) UpdateSlo(ctx context.Context, projectID, id string, in SloUpdate) (*SloRead, error) {
	path := apiPath("projects", projectID, "slos", id)
	return request[SloRead](ctx, c, http.MethodPatch, path, in)
}

func (c *APIClient) DeleteSlo(ctx context.Context, projectID, id string) error {
	return c.remove(ctx, apiPath("projects", projectID, "slos", id))
}

func (c *APIClient) ListSlos(ctx context.Context, projectID string) ([]SloRead, error) {
	return list[SloRead](ctx, c, apiPath("projects", projectID, "slos"))
}

// Schedules are organization-scoped. The API has no list route: a schedule is
// addressed by its ID. Update is a partial PUT that changes only the fields it
// names.

// ScheduleWindow is a time window in which a schedule delivers. Days are ISO
// weekday numbers (1 is Monday, 7 is Sunday) and times are 24-hour "HH:MM".
type ScheduleWindow struct {
	Days      []int64 `json:"days"`
	StartTime string  `json:"start_time"`
	EndTime   string  `json:"end_time"`
}

type ScheduleRead struct {
	ID             string           `json:"id"`
	OrganizationID string           `json:"organization_id"`
	Label          string           `json:"label"`
	Timezone       string           `json:"timezone"`
	Windows        []ScheduleWindow `json:"windows"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      *string          `json:"updated_at"`
}

type ScheduleCreate struct {
	Label    string           `json:"label"`
	Timezone string           `json:"timezone"`
	Windows  []ScheduleWindow `json:"windows"`
}

type ScheduleUpdate struct {
	Label    *string           `json:"label,omitempty"`
	Timezone *string           `json:"timezone,omitempty"`
	Windows  *[]ScheduleWindow `json:"windows,omitempty"`
}

const (
	notificationWindowsPath = "/api/v1/notification-windows/"
	legacySchedulesPath     = "/api/v1/schedules/"
)

// doSchedule calls the notification windows route. An instance that predates
// that route answers FastAPI's missing-route 404, and doSchedule then calls the
// deprecated /schedules/ route Logfire served before. A 404 never has an
// effect, so the second call is safe for every method. A 404 for a missing
// window is final, so a failed second call cannot hide it. Once the deprecated
// route has answered, later calls skip the new one. A POST addresses the
// collection and every other method addresses the item id.
func (c *APIClient) doSchedule(ctx context.Context, method, id string, in, out any, expected int) (*http.Response, error) {
	suffix := ""
	if method != http.MethodPost {
		suffix = url.PathEscape(id) + "/"
	}
	if !c.shared.legacySchedules.Load() {
		resp, err := c.doJSON(ctx, method, notificationWindowsPath+suffix, in, out, expected)
		// A route that looks up an item answers a missing item with its own
		// detail, while FastAPI answers a missing route with "Not Found" and a
		// proxy may send no detail at all.
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			return resp, err
		}
		var body struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal([]byte(apiErr.Message), &body)
		if body.Detail != "" && body.Detail != "Not Found" {
			return resp, err
		}
	}
	resp, err := c.doJSON(ctx, method, legacySchedulesPath+suffix, in, out, expected)
	if err == nil {
		c.shared.legacySchedules.Store(true)
	}
	return resp, err
}

// CreateSchedule is not retried automatically: schedules have no unique
// field, so a retry after a create that reached the server would add a
// second schedule.
func (c *APIClient) CreateSchedule(ctx context.Context, in ScheduleCreate) (*ScheduleRead, error) {
	var out ScheduleRead
	ctx = disableAutomaticRetries(ctx)
	if _, err := c.doSchedule(ctx, http.MethodPost, "", in, &out, http.StatusCreated); err != nil {
		// Schedules have no list route and unknown ids live under /{id}/, so a
		// 404 on a create from both routes means the instance predates the
		// schedules API rather than an unknown schedule.
		return nil, unavailableIfNotFound(
			err, http.MethodPost, notificationWindowsPath, "v2026-09-23.01", "logfire-0.13.47")
	}
	return &out, nil
}

func (c *APIClient) GetSchedule(ctx context.Context, id string) (*ScheduleRead, int, error) {
	var out ScheduleRead
	resp, err := c.doSchedule(ctx, http.MethodGet, id, nil, &out, http.StatusOK)
	return withStatus(&out, resp, err)
}

func (c *APIClient) UpdateSchedule(ctx context.Context, id string, in ScheduleUpdate) (*ScheduleRead, error) {
	var out ScheduleRead
	if _, err := c.doSchedule(ctx, http.MethodPut, id, in, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) DeleteSchedule(ctx context.Context, id string) error {
	_, err := c.doSchedule(ctx, http.MethodDelete, id, nil, nil, http.StatusNoContent)
	return err
}
