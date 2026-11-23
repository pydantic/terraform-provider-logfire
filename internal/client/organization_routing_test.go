// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

const routingBaseURL = "https://logfire.example.invalid"

// contextScopes is the scope set that an organization context token must hold.
// It leaves out the scopes the exchange refuses and the gateway scopes that
// older instances reject.
var contextScopes = []string{
	"organization:read", "organization:write", "organization:create_project",
	"organization:read_member", "organization:write_member",
	"organization:read_channel", "organization:write_channel",
	"organization:create_api_key", "organization:read_api_key", "organization:write_api_key",
	"project:read", "project:write", "project:read_token", "project:write_token",
	"project:read_dashboard", "project:write_dashboard", "project:read_alert", "project:write_alert",
}

type loggedRequest struct {
	method        string
	path          string
	authorization string
	form          url.Values
}

// instanceFake answers the token endpoint with a distinct token for every
// exchange ("token-<organization>-<n>") and records every request.
type instanceFake struct {
	mu        sync.Mutex
	requests  []loggedRequest
	exchanges int
	// exchangeFailure is the body of a 400 answer from the token endpoint.
	exchangeFailure string
	// unauthorized is the number of organization requests answered with 401.
	unauthorized int
}

func (f *instanceFake) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	logged := loggedRequest{method: req.Method, path: req.URL.Path, authorization: req.Header.Get("Authorization")}
	respond := func(status int, body string) (*http.Response, error) {
		f.requests = append(f.requests, logged)
		return &http.Response{StatusCode: status, Header: make(http.Header), Request: req,
			Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	switch {
	case req.URL.Path == "/api/oauth/token":
		logged.form = formBody(req)
		if f.exchangeFailure != "" {
			return respond(http.StatusBadRequest, f.exchangeFailure)
		}
		f.exchanges++
		organization := logged.form.Get("audience")[strings.LastIndex(logged.form.Get("audience"), "/")+1:]
		return respond(http.StatusOK, fmt.Sprintf(`{"access_token":"token-%s-%d","expires_in":900}`, organization, f.exchanges))
	case strings.HasPrefix(req.URL.Path, instanceRoutePrefix):
		if req.Method == http.MethodGet {
			return respond(http.StatusOK, `[]`)
		}
		return respond(http.StatusCreated, `{"id":"org-1","organization_name":"acme"}`)
	case f.unauthorized > 0:
		f.unauthorized--
		return respond(http.StatusUnauthorized, `{"detail":"expired"}`)
	case req.Method == http.MethodPost && req.URL.Path == "/api/v1/projects/":
		return respond(http.StatusCreated, `{"id":"project-1","project_name":"svc","organization_name":"acme"}`)
	default:
		return respond(http.StatusOK, `{"id":"project-1","project_name":"svc","organization_name":"acme"}`)
	}
}

func (f *instanceFake) exchangeRequests() []loggedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []loggedRequest
	for _, r := range f.requests {
		if r.path == "/api/oauth/token" {
			out = append(out, r)
		}
	}
	return out
}

func (f *instanceFake) apiRequests() []loggedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []loggedRequest
	for _, r := range f.requests {
		if r.path != "/api/oauth/token" {
			out = append(out, r)
		}
	}
	return out
}

func newRoutingClient(t *testing.T) (*APIClient, *instanceFake) {
	t.Helper()
	fake := &instanceFake{}
	c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: fake})
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

func sortedScopes(scope string) []string {
	fields := strings.Fields(scope)
	slices.Sort(fields)
	return fields
}

func TestOrganizationClientCreatesProjectWithExchangedToken(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)
	acme := c.ForOrganization("acme")

	if _, err := acme.CreateProject(context.Background(), ProjectCreate{ProjectName: "svc"}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	exchanges := fake.exchangeRequests()
	if len(exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(exchanges))
	}
	form := exchanges[0].form
	if form.Get("subject_token") != "admin-key" {
		t.Fatalf("subject_token = %q, want the raw key", form.Get("subject_token"))
	}
	if want := routingBaseURL + "/acme"; form.Get("audience") != want {
		t.Fatalf("audience = %q, want %q", form.Get("audience"), want)
	}
	want := slices.Clone(contextScopes)
	slices.Sort(want)
	if got := sortedScopes(form.Get("scope")); !slices.Equal(got, want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}

	calls := fake.apiRequests()
	if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].path != "/api/v1/projects/" {
		t.Fatalf("api requests = %+v, want one POST /api/v1/projects/", calls)
	}
	if calls[0].authorization != "Bearer token-acme-1" {
		t.Fatalf("Authorization = %q, want the exchanged token", calls[0].authorization)
	}
	if c.Organization() != "" || acme.Organization() != "acme" {
		t.Fatalf("organizations = %q and %q, want an unchanged parent and acme", c.Organization(), acme.Organization())
	}
}

func TestOrganizationClientKeepsCredentialOnInstanceRoutes(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)
	acme := c.ForOrganization("acme")

	if _, err := acme.ListOrganizations(context.Background()); err != nil {
		t.Fatalf("list organizations: %v", err)
	}
	if _, err := acme.CreateOrganization(context.Background(), OrganizationCreate{OrganizationName: "acme"}); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if got := len(fake.exchangeRequests()); got != 0 {
		t.Fatalf("instance routes made %d exchanges, want none", got)
	}
	for _, call := range fake.apiRequests() {
		if call.authorization != "Bearer admin-key" {
			t.Fatalf("%s %s sent %q, want the raw key", call.method, call.path, call.authorization)
		}
	}

	// A project route on the same client does exchange, so the checks above could see one.
	if _, _, err := acme.GetProject(context.Background(), "project-1"); err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got := len(fake.exchangeRequests()); got != 1 {
		t.Fatalf("exchanges after a project request = %d, want 1", got)
	}
}

func TestOrganizationClientWithoutOrganizationNeverExchanges(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)

	for name, client := range map[string]*APIClient{
		"parent":                  c,
		"empty name":              c.ForOrganization(""),
		"reset after an exchange": c.ForOrganization("acme").ForOrganization(""),
	} {
		if client.Organization() != "" {
			t.Fatalf("%s: organization = %q, want none", name, client.Organization())
		}
		if _, _, err := client.GetProject(context.Background(), "project-1"); err != nil {
			t.Fatalf("%s: get project: %v", name, err)
		}
	}
	if got := len(fake.exchangeRequests()); got != 0 {
		t.Fatalf("exchanges = %d, want none", got)
	}
	for _, call := range fake.apiRequests() {
		if call.authorization != "Bearer admin-key" {
			t.Fatalf("%s %s sent %q, want the raw key", call.method, call.path, call.authorization)
		}
	}
}

func TestOrganizationContextTokensCacheByOrganizationAndScopes(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)
	ctx := context.Background()
	acme := c.ForOrganization("acme")

	for range 2 {
		if _, _, err := c.GetOrganizationContext(ctx, "acme"); err != nil {
			t.Fatalf("get organization: %v", err)
		}
		if _, _, err := acme.GetProject(ctx, "project-1"); err != nil {
			t.Fatalf("get project: %v", err)
		}
	}
	// A derived client shares the cache with the one it came from.
	if _, _, err := c.ForOrganization("acme").GetProject(ctx, "project-1"); err != nil {
		t.Fatalf("get project: %v", err)
	}
	if _, _, err := c.ForOrganization("globex").GetProject(ctx, "project-1"); err != nil {
		t.Fatalf("get project in globex: %v", err)
	}

	var got []string
	for _, exchange := range fake.exchangeRequests() {
		got = append(got, exchange.form.Get("audience")+" "+fmt.Sprint(len(strings.Fields(exchange.form.Get("scope")))))
	}
	want := []string{
		routingBaseURL + "/acme 2",
		fmt.Sprintf("%s/acme %d", routingBaseURL, len(contextScopes)),
		fmt.Sprintf("%s/globex %d", routingBaseURL, len(contextScopes)),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("exchanges (audience, scope count) = %v, want %v", got, want)
	}
}

func TestOrganizationContextRetriesOnceAfterUnauthorized(t *testing.T) {
	t.Parallel()
	t.Run("a new token succeeds", func(t *testing.T) {
		t.Parallel()
		c, fake := newRoutingClient(t)
		fake.unauthorized = 1
		if _, _, err := c.ForOrganization("acme").GetProject(context.Background(), "project-1"); err != nil {
			t.Fatalf("get project: %v", err)
		}
		var bearers []string
		for _, call := range fake.apiRequests() {
			bearers = append(bearers, call.authorization)
		}
		if want := []string{"Bearer token-acme-1", "Bearer token-acme-2"}; !slices.Equal(bearers, want) {
			t.Fatalf("bearers = %v, want a retry with a fresh exchange %v", bearers, want)
		}
		if got := len(fake.exchangeRequests()); got != 2 {
			t.Fatalf("exchanges = %d, want 2", got)
		}
	})
	t.Run("a second 401 is final", func(t *testing.T) {
		t.Parallel()
		c, fake := newRoutingClient(t)
		fake.unauthorized = 10
		_, status, err := c.ForOrganization("acme").GetProject(context.Background(), "project-1")
		if err == nil || status != http.StatusUnauthorized {
			t.Fatalf("get project: status=%d err=%v, want a 401 error", status, err)
		}
		if got := len(fake.apiRequests()); got != 2 {
			t.Fatalf("api requests = %d, want the original and one retry", got)
		}
	})
}

func TestOrganizationContextNamesInstanceAdminRequirement(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)
	fake.exchangeFailure = `{"error":"invalid_scope","error_description":"subject_token must include instance:admin"}`

	_, status, err := c.ForOrganization("acme").GetProject(context.Background(), "project-1")
	if err == nil || status != 0 {
		t.Fatalf("get project: status=%d err=%v, want an exchange error without a status", status, err)
	}
	if !IsInstanceAdminRequiredError(err) {
		t.Fatalf("error %q is not an instance-admin error", err)
	}
	for _, want := range []string{`"acme"`, "instance-admin API key", "admin organization", "subject_token must include instance:admin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	var oauthErr *OAuthTokenError
	if !errors.As(err, &oauthErr) || oauthErr.Code != "invalid_scope" {
		t.Fatalf("error %q does not unwrap to the OAuth answer", err)
	}
	if IsInvalidTargetError(err) {
		t.Fatalf("error %q is classified as invalid_target", err)
	}
	// The organization resource goes through the same exchange.
	if _, _, err := c.GetOrganizationContext(context.Background(), "acme"); !IsInstanceAdminRequiredError(err) {
		t.Fatalf("organization read error %v is not an instance-admin error", err)
	}
}

func TestOrganizationContextKeepsOtherExchangeErrors(t *testing.T) {
	t.Parallel()
	for name, failure := range map[string]string{
		"another invalid_scope":    `{"error":"invalid_scope","error_description":"unknown scope: organization:read_gateway"}`,
		"unknown organization":     `{"error":"invalid_target","error_description":"unknown organization: 'acme'"}`,
		"audience from a bad host": `{"error":"invalid_target","error_description":"audience does not match the configured OIDC base URL"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, fake := newRoutingClient(t)
			fake.exchangeFailure = failure
			_, _, err := c.ForOrganization("acme").GetProject(context.Background(), "project-1")
			if err == nil || IsInstanceAdminRequiredError(err) {
				t.Fatalf("error = %v, want the plain exchange error", err)
			}
			var oauthErr *OAuthTokenError
			if !errors.As(err, &oauthErr) {
				t.Fatalf("error %v is not an OAuth error", err)
			}
			if wantTarget := strings.Contains(failure, "invalid_target"); IsInvalidTargetError(err) != wantTarget {
				t.Fatalf("IsInvalidTargetError(%v) = %v, want %v", err, !wantTarget, wantTarget)
			}
		})
	}
}

// Every organization-scoped request helper goes through the exchange, so one
// token serves a request of each kind.
func TestOrganizationClientExchangesForEveryOrganizationRoute(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)
	acme := c.ForOrganization("acme")
	ctx := context.Background()

	calls := map[string]func(){
		"projects":              func() { _, _ = acme.ListProjects(ctx) },
		"alerts":                func() { _, _ = acme.ListAlerts(ctx, "p") },
		"channels":              func() { _, _ = acme.ListChannels(ctx) },
		"dashboards":            func() { _, _ = acme.ListDashboards(ctx, "p") },
		"slos":                  func() { _, _ = acme.ListSlos(ctx, "p") },
		"read tokens":           func() { _, _ = acme.ListReadTokens(ctx, "p") },
		"write tokens":          func() { _, _ = acme.ListWriteTokens(ctx, "p") },
		"frontend applications": func() { _, _ = acme.ListFrontendApplications(ctx, "p") },
		"schedules":             func() { _, _, _ = acme.GetSchedule(ctx, "s") },
		"api keys":              func() { _, _ = acme.ListAPIKeys(ctx) },
		"gateway providers":     func() { _, _ = acme.ListGatewayProviders(ctx) },
		"gateway endpoints":     func() { _, _ = acme.GetGatewayEndpoint(ctx, "e") },
		"spending limits":       func() { _, _ = acme.GetGatewayOrganizationSpendingLimits(ctx) },
		"pagerduty":             func() { _, _ = acme.GetPagerDutyService(ctx, "acct", "us", "svc") },
	}
	for _, call := range calls {
		call()
	}

	if got := len(fake.exchangeRequests()); got != 1 {
		t.Fatalf("exchanges = %d, want one shared token", got)
	}
	api := fake.apiRequests()
	if len(api) < len(calls) {
		t.Fatalf("recorded %d api requests for %d calls", len(api), len(calls))
	}
	for _, call := range api {
		if call.authorization != "Bearer token-acme-1" {
			t.Fatalf("%s %s sent %q, want the exchanged token", call.method, call.path, call.authorization)
		}
	}
}
