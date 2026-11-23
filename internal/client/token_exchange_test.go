// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A token endpoint that the instance does not serve answers 404 without an
// OAuth envelope. Callers read a 404 as "the resource is gone" and drop it from
// state, so the exchange failure must never look like one.
func TestExchangeNotFoundIsNotAResourceNotFound(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != tokenExchangePath {
			t.Errorf("request reached %s %s without a token", req.Method, req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Request: req,
			Body: io.NopCloser(strings.NewReader(`{"detail":"Not Found"}`))}, nil
	})
	c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	team := c.ForOrganization("team-a")

	_, status, err := team.GetProject(t.Context(), "project-1")
	if err == nil || status == http.StatusNotFound || IsNotFoundError(err) {
		t.Fatalf("GetProject = status %d, err %v; want an error that is not a not-found", status, err)
	}
	if !strings.Contains(err.Error(), tokenExchangePath) {
		t.Fatalf("error %q does not name the token endpoint", err)
	}
	if err := team.DeleteProject(t.Context(), "project-1"); err == nil || IsNotFoundError(err) {
		t.Fatalf("DeleteProject error = %v, want an error that is not a not-found", err)
	}
	if _, err := team.ListWriteTokens(t.Context(), "project-1"); err == nil || IsNotFoundError(err) {
		t.Fatalf("ListWriteTokens error = %v, want an error that is not a not-found", err)
	}
}

// Terraform runs up to ten operations at once. On a cold cache they must share
// one exchange for an organization, because the token endpoint allows 20
// exchanges a minute from one address.
func TestConcurrentRequestsShareOneExchange(t *testing.T) {
	t.Parallel()
	var exchanges atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		respond := func(body string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req,
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		if req.URL.Path == tokenExchangePath {
			exchanges.Add(1)
			time.Sleep(100 * time.Millisecond)
			return respond(`{"access_token":"org-token","expires_in":900}`)
		}
		if req.Header.Get("Authorization") != "Bearer org-token" {
			t.Errorf("%s %s sent %q", req.Method, req.URL.Path, req.Header.Get("Authorization"))
		}
		return respond(`{"id":"project-1","project_name":"svc","organization_name":"team-a"}`)
	})
	c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, _, err := c.ForOrganization("team-a").GetProject(t.Context(), "project-1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := exchanges.Load(); got != 1 {
		t.Fatalf("10 concurrent requests made %d exchanges, want 1", got)
	}
}

// The exchange caps expires_in at the instance's JWT max age and rejects a
// larger value. An instance with a max age under 15 minutes must still work.
func TestExchangeOmitsExpiresInWhenTheInstanceAllowsLess(t *testing.T) {
	t.Parallel()
	var forms []string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		respond := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Request: req,
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		if req.URL.Path != tokenExchangePath {
			return respond(http.StatusOK, `{"id":"project-1","project_name":"svc","organization_name":"team-a"}`)
		}
		form := formBody(req)
		forms = append(forms, form.Encode())
		if form.Has("expires_in") && form.Get("expires_in") != "300" {
			return respond(http.StatusBadRequest,
				`{"error":"invalid_request","error_description":"expires_in must be at most 300 seconds"}`)
		}
		return respond(http.StatusOK, `{"access_token":"org-token","expires_in":300}`)
	})
	c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ForOrganization("team-a").GetProject(t.Context(), "project-1"); err != nil {
		t.Fatalf("an instance with a 5-minute max age refused the exchange: %v", err)
	}
	if len(forms) != 2 || strings.Contains(forms[1], "expires_in") {
		t.Fatalf("exchange forms = %q, want a retry without expires_in", forms)
	}
	if _, _, err := c.ForOrganization("team-a").GetProject(t.Context(), "project-1"); err != nil || len(forms) != 2 {
		t.Fatalf("second request: err %v, %d exchanges; want the cached token", err, len(forms))
	}
}

// Other invalid_request answers are not about the lifetime and stay errors.
func TestExchangeKeepsOtherInvalidRequestErrors(t *testing.T) {
	t.Parallel()
	exchanges := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		exchanges++
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Request: req,
			Body: io.NopCloser(strings.NewReader(
				`{"error":"invalid_request","error_description":"instance-admin token exchange is only available for on-prem deployments"}`))}, nil
	})
	c, err := NewAPIClient(routingBaseURL, "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.ForOrganization("team-a").GetProject(t.Context(), "project-1")
	var oauthErr *OAuthTokenError
	if !errors.As(err, &oauthErr) || oauthErr.Code != "invalid_request" || exchanges != 1 {
		t.Fatalf("err = %v after %d exchanges, want one invalid_request answer", err, exchanges)
	}
}

// The exchange form carries the instance-admin key. A redirect must never carry
// it to another host.
func TestExchangeRefusesRedirectToAnotherHost(t *testing.T) {
	t.Parallel()
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "admin-key") {
			leaked.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"stolen","expires_in":900}`))
	}))
	t.Cleanup(other.Close)
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+tokenExchangePath, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(instance.Close)

	c, err := NewAPIClient(instance.URL, "admin-key", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.ForOrganization("team-a").GetProject(t.Context(), "project-1")
	if leaked.Load() != 0 {
		t.Fatal("the exchange sent the API key to the redirect target")
	}
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v, want an error about the redirect", err)
	}
}

// A redirect that stays on the instance's host, such as an added trailing
// slash, keeps working.
func TestExchangeFollowsRedirectOnTheSameHost(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc(tokenExchangePath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tokenExchangePath+"/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc(tokenExchangePath+"/", func(w http.ResponseWriter, r *http.Request) {
		if formBody(r).Get("subject_token") != "admin-key" {
			t.Errorf("the redirected exchange lost its form")
		}
		_, _ = w.Write([]byte(`{"access_token":"org-token","expires_in":900}`))
	})
	mux.HandleFunc("/api/v1/projects/project-1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer org-token" {
			t.Errorf("project request sent %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":"project-1","project_name":"svc","organization_name":"team-a"}`))
	})
	instance := httptest.NewServer(mux)
	t.Cleanup(instance.Close)

	c, err := NewAPIClient(instance.URL, "admin-key", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ForOrganization("team-a").GetProject(t.Context(), "project-1"); err != nil {
		t.Fatalf("a same-host redirect broke the exchange: %v", err)
	}
}
