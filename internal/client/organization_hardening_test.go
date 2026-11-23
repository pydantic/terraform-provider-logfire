// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func jsonResponse(req *http.Request, status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status, Header: make(http.Header), Request: req,
		Body: io.NopCloser(strings.NewReader(body)),
	}, nil
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// A credentialed request must not follow a redirect that carries the key or the
// custom headers to another host, or from HTTPS to HTTP.
func TestCredentialedRequestsRefuseUnsafeRedirects(t *testing.T) {
	t.Parallel()
	var leaked atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Proxy-Secret") != "" {
			leaked.Add(1)
		}
		_, _ = w.Write([]byte(`{"id":"project-1","project_name":"svc","organization_name":"acme"}`))
	}))
	t.Cleanup(plain.Close)
	otherHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+portOf(t, plain.URL)+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(otherHost.Close)
	downgrade := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(downgrade.Close)

	for name, tc := range map[string]struct {
		baseURL string
		http    *http.Client
	}{
		"another host":  {baseURL: otherHost.URL, http: &http.Client{}},
		"https to http": {baseURL: downgrade.URL, http: downgrade.Client()},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := NewAPIClient(tc.baseURL, "admin-key", tc.http, WithAdditionalHeaders(http.Header{"X-Proxy-Secret": {"s3cret"}}))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = c.GetProject(t.Context(), "project-1")
			if err == nil || !strings.Contains(err.Error(), "redirect") {
				t.Fatalf("err = %v, want an error about the redirect", err)
			}
			if leaked.Load() != 0 {
				t.Fatal("the request carried its credentials to the redirect target")
			}
		})
	}
}

func TestSameHostRedirectRule(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		from, to string
		allowed  bool
	}{
		"same origin":                    {from: "https://h/a", to: "https://h/b", allowed: true},
		"explicit default port":          {from: "https://h/a", to: "https://h:443/b", allowed: true},
		"explicit default port, http":    {from: "http://h:80/a", to: "http://h/b", allowed: true},
		"upgrade to https":               {from: "http://h/a", to: "https://h/b", allowed: true},
		"upgrade to https on a new port": {from: "http://h:8080/a", to: "https://h:8443/b", allowed: true},
		"host case":                      {from: "https://H/a", to: "https://h/b", allowed: true},
		"downgrade to http":              {from: "https://h/a", to: "http://h/b"},
		"downgrade on another port":      {from: "https://h/a", to: "http://h:8080/b"},
		"other host":                     {from: "https://h/a", to: "https://other/b"},
		"other port":                     {from: "https://h/a", to: "https://h:8443/b"},
		"subdomain":                      {from: "https://h/a", to: "https://api.h/b"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, _ := http.NewRequest(http.MethodGet, tc.from, nil)
			next, _ := http.NewRequest(http.MethodGet, tc.to, nil)
			err := sameHostRedirect(next, []*http.Request{first})
			if (err == nil) != tc.allowed {
				t.Fatalf("redirect %s to %s: err = %v, allowed = %v", tc.from, tc.to, err, tc.allowed)
			}
		})
	}
}

// One caller that gives up must not fail the callers that share its exchange.
func TestCancelledCallerDoesNotFailSharedExchange(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/oauth/token" {
			return jsonResponse(req, http.StatusOK, `{"id":"project-1","project_name":"svc","organization_name":"acme"}`)
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return jsonResponse(req, http.StatusOK, `{"access_token":"token-acme","expires_in":900}`)
	})
	c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	acme := c.ForOrganization("acme")

	first, cancel := context.WithCancel(t.Context())
	firstErr := make(chan error, 1)
	go func() {
		_, _, err := acme.GetProject(first, "project-1")
		firstErr <- err
	}()
	<-entered
	secondErr := make(chan error, 1)
	go func() {
		_, _, err := acme.GetProject(t.Context(), "project-1")
		secondErr <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: err = %v, want context canceled", err)
	}
	close(release)
	if err := <-secondErr; err != nil {
		t.Fatalf("waiting caller failed with %v after the first caller was cancelled", err)
	}
}

// A relist asks for a list that is newer than the call. A list already in
// flight began before it, so it can miss an organization created since.
func TestRelistDoesNotJoinAnOlderList(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	names := []string{}
	held := true
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		snapshot := append([]string(nil), names...)
		hold := held
		held = false
		mu.Unlock()
		if hold {
			arrived <- struct{}{}
			<-release
		}
		var items []string
		for _, name := range snapshot {
			items = append(items, fmt.Sprintf(`{"id":"id-%s","organization_name":%q}`, name, name))
		}
		return jsonResponse(req, http.StatusOK, "["+strings.Join(items, ",")+"]")
	})
	c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = c.Organizations(t.Context(), false) }()
	<-arrived
	mu.Lock()
	names = []string{"created-later"}
	mu.Unlock()

	relisted := make(chan []OrganizationRead, 1)
	go func() {
		list, _ := c.Organizations(t.Context(), true)
		relisted <- list
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)

	list := <-relisted
	if len(list) != 1 || list[0].OrganizationName != "created-later" {
		t.Fatalf("relisted organizations = %+v, want the one created after the first list began", list)
	}
}

func TestForgetOrganizationDropsTheListAndTheTokens(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)
	ctx := t.Context()
	acme := c.ForOrganization("acme")
	countLists := func() int {
		n := 0
		for _, call := range fake.apiRequests() {
			if call.path == "/api/v1/instance/organizations/" {
				n++
			}
		}
		return n
	}
	if _, _, err := acme.GetProject(ctx, "project-1"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := c.Organizations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(fake.exchangeRequests()); got != 1 || countLists() != 1 {
		t.Fatalf("exchanges = %d, lists = %d, want one of each", got, countLists())
	}

	c.ForgetOrganization("acme")
	if _, _, err := acme.GetProject(ctx, "project-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Organizations(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.exchangeRequests()); got != 2 {
		t.Fatalf("exchanges after the organization changed = %d, want a new token", got)
	}
	if got := countLists(); got != 2 {
		t.Fatalf("organization lists = %d, want the list fetched again", got)
	}
	if _, _, err := c.ForOrganization("globex").GetProject(ctx, "project-1"); err != nil {
		t.Fatal(err)
	}
	c.ForgetOrganization("acme")
	if _, _, err := c.ForOrganization("globex").GetProject(ctx, "project-1"); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.exchangeRequests()); got != 3 {
		t.Fatalf("exchanges = %d, want globex's token kept when acme changes", got)
	}
}

// A caller whose token was rejected must not exchange again when another
// caller already replaced that token. The exchange endpoint allows 20
// exchanges a minute from one address.
func TestRejectedTokenIsReplacedOnce(t *testing.T) {
	t.Parallel()
	c, fake := newRoutingClient(t)
	ctx := t.Context()

	var holding sync.WaitGroup
	holding.Add(2)
	firstDone := make(chan struct{})
	call := func(waitForFirst bool) func(token string) (*http.Response, error) {
		var held string
		return func(token string) (*http.Response, error) {
			if held == "" {
				held = token
				holding.Done()
				holding.Wait()
				if waitForFirst {
					<-firstDone
				}
			}
			if token == "token-acme-1" {
				return &http.Response{StatusCode: http.StatusUnauthorized}, &APIError{StatusCode: http.StatusUnauthorized}
			}
			return &http.Response{StatusCode: http.StatusOK}, nil
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(firstDone)
		if _, err := c.withOrganizationContext(ctx, "acme", organizationContextScopes, call(false)); err != nil {
			t.Errorf("first caller: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := c.withOrganizationContext(ctx, "acme", organizationContextScopes, call(true)); err != nil {
			t.Errorf("second caller: %v", err)
		}
	}()
	wg.Wait()
	if got := len(fake.exchangeRequests()); got != 2 {
		t.Fatalf("exchanges = %d, want the first token and one replacement", got)
	}
}

func TestOwnOrganization(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status       int
		wantOrg      string
		wantErr      bool
		wantRequests int32
	}{
		"readable":    {status: http.StatusOK, wantOrg: "acme", wantRequests: 1},
		"forbidden":   {status: http.StatusForbidden, wantRequests: 1},
		"no route":    {status: http.StatusNotFound, wantRequests: 1},
		"unavailable": {status: http.StatusInternalServerError, wantErr: true, wantRequests: 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var requests, withRawKey atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				if req.URL.Path == "/api/v1/organization/" && req.Header.Get("Authorization") == "Bearer admin-key" {
					withRawKey.Add(1)
				}
				return jsonResponse(req, tc.status, `{"id":"org-1","organization_name":"acme"}`)
			})
			c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			derived := c.ForOrganization("globex")
			var org *OrganizationRead
			for range 2 {
				org, err = derived.OwnOrganization(t.Context())
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			switch {
			case org == nil && tc.wantOrg != "":
				t.Fatalf("organization = nil, want %q", tc.wantOrg)
			case org != nil && org.OrganizationName != tc.wantOrg:
				t.Fatalf("organization = %q, want %q", org.OrganizationName, tc.wantOrg)
			}
			if requests.Load() != tc.wantRequests || withRawKey.Load() != tc.wantRequests {
				t.Fatalf("requests = %d (with the raw key: %d), want %d with the raw key and no exchange",
					requests.Load(), withRawKey.Load(), tc.wantRequests)
			}
		})
	}
}

func TestIsExchangeRefusalError(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"not an instance-admin key": {err: &InstanceAdminRequiredError{Err: &OAuthTokenError{Code: "invalid_scope"}}, want: true},
		"unknown organization":      {err: &OAuthTokenError{Code: "invalid_target"}, want: true},
		"on-prem only":              {err: &OAuthTokenError{Code: "invalid_request"}, want: true},
		"no token endpoint":         {err: &EndpointUnavailableError{}, want: true},
		"wrapped refusal":           {err: fmt.Errorf("x: %w", &OAuthTokenError{Code: "invalid_scope"}), want: true},
		"server error":              {err: &OAuthTokenError{Code: "server_error"}},
		"temporarily unavailable":   {err: &OAuthTokenError{Code: "temporarily_unavailable"}},
		"timeout":                   {err: context.DeadlineExceeded},
		"gateway answer":            {err: errors.New("token exchange failed: API error (status 502)")},
		"api error":                 {err: &APIError{StatusCode: 500}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := IsExchangeRefusalError(tc.err); got != tc.want {
				t.Fatalf("IsExchangeRefusalError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
