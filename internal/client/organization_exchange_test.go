// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// exchangeInstance serves the token endpoint and the routes that the tests call.
// An exchange mints a token named after its audience and a counter.
type exchangeInstance struct {
	*httptest.Server
	mu        sync.Mutex
	exchanges map[string]int
	forms     []map[string]string
	// exchangeStatus answers the first exchanges with a status other than 200.
	exchangeStatus []int
	exchangeBody   string
	exchangeDelay  time.Duration
	// rejectToken makes the project routes answer 401 for the tokens with this prefix.
	rejectToken atomic.Value
	seenAuth    []string
	creates     atomic.Int32
}

func newExchangeInstance(t *testing.T) *exchangeInstance {
	t.Helper()
	in := &exchangeInstance{exchanges: map[string]int{}}
	in.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == tokenExchangePath:
			_ = r.ParseForm()
			audience := r.PostForm.Get("audience")
			in.mu.Lock()
			form := map[string]string{}
			for k := range r.PostForm {
				form[k] = r.PostForm.Get(k)
			}
			in.forms = append(in.forms, form)
			in.exchanges[audience]++
			n := in.exchanges[audience]
			var status int
			if len(in.exchangeStatus) > 0 {
				status, in.exchangeStatus = in.exchangeStatus[0], in.exchangeStatus[1:]
			}
			body, delay := in.exchangeBody, in.exchangeDelay
			in.mu.Unlock()
			time.Sleep(delay)
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			_, _ = fmt.Fprintf(w, `{"access_token":"%s#%d","expires_in":900}`, audience, n)
		case r.URL.Path == "/api/v1/instance/organizations/":
			in.record(r)
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/projects/" && r.Method == http.MethodPost:
			in.record(r)
			in.creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"p1","project_name":"svc"}`))
		case r.URL.Path == "/api/v1/projects/":
			in.record(r)
			if rejected, _ := in.rejectToken.Load().(string); rejected != "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+rejected) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"detail":"expired"}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(in.Close)
	return in
}

func (in *exchangeInstance) record(r *http.Request) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.seenAuth = append(in.seenAuth, r.URL.Path+" "+r.Header.Get("Authorization"))
}

func (in *exchangeInstance) exchangeCount(organization string) int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.exchanges[in.URL+"/"+organization]
}

func (in *exchangeInstance) client(t *testing.T, httpClient *http.Client) *APIClient {
	t.Helper()
	c, err := NewAPIClient(in.URL, "admin-key", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOrganizationClientExchangesForTheOrganizationRoutes(t *testing.T) {
	t.Parallel()
	in := newExchangeInstance(t)
	c := in.client(t, nil).ForOrganization("team-a")
	if _, err := c.ListProjects(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListOrganizations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(in.seenAuth, "\n"),
		"/api/v1/projects/ Bearer "+in.URL+"/team-a#1\n/api/v1/instance/organizations/ Bearer admin-key"; got != want {
		t.Fatalf("authorization per route:\n%s\nwant:\n%s", got, want)
	}
	form := in.forms[0]
	if form["subject_token"] != "admin-key" || form["audience"] != in.URL+"/team-a" || form["scope"] != organizationContextScopes {
		t.Fatalf("exchange form = %v", form)
	}
}

func TestOrganizationClientWithoutOrganizationSendsTheKey(t *testing.T) {
	t.Parallel()
	in := newExchangeInstance(t)
	if _, err := in.client(t, nil).ListProjects(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(in.forms) != 0 || in.seenAuth[0] != "/api/v1/projects/ Bearer admin-key" {
		t.Fatalf("exchanges = %d, requests = %v; want the key and no exchange", len(in.forms), in.seenAuth)
	}
}

func TestConcurrentRequestsShareOneExchangePerOrganization(t *testing.T) {
	t.Parallel()
	in := newExchangeInstance(t)
	in.exchangeDelay = 100 * time.Millisecond
	base := in.client(t, nil)
	var wg sync.WaitGroup
	for i := range 40 {
		organization := []string{"team-a", "team-b"}[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := base.ForOrganization(organization).ListProjects(t.Context()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, organization := range []string{"team-a", "team-b"} {
		if got := in.exchangeCount(organization); got != 1 {
			t.Errorf("exchanges for %s = %d, want 1", organization, got)
		}
	}
	for _, line := range in.seenAuth {
		if !strings.Contains(line, "Bearer "+in.URL+"/team-") {
			t.Errorf("request without an exchanged token: %s", line)
		}
	}
}

func TestRejectedTokenIsReplacedOnce(t *testing.T) {
	t.Parallel()
	in := newExchangeInstance(t)
	c := in.client(t, nil).ForOrganization("team-a")
	if _, err := c.ListProjects(t.Context()); err != nil {
		t.Fatal(err)
	}
	in.rejectToken.Store(in.URL + "/team-a#1")
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ListProjects(t.Context()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := in.exchangeCount("team-a"); got != 2 {
		t.Fatalf("exchanges = %d, want the first and one replacement", got)
	}

	in.rejectToken.Store(in.URL + "/team-a#")
	_, err := c.ListProjects(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v, want the 401 after one retry", err)
	}
	if got := in.exchangeCount("team-a"); got != 3 {
		t.Fatalf("exchanges = %d, want one more and no loop", got)
	}
}

func retryingClient(t *testing.T, in *exchangeInstance) *APIClient {
	t.Helper()
	return in.client(t, &http.Client{Transport: &retryingTransport{
		next: http.DefaultTransport, maxAttempts: defaultRetryMaxAttempts, baseDelay: time.Millisecond, maxDelay: 2 * time.Millisecond,
	}})
}

func TestCreateWaitsForAnExchangeThatRetries(t *testing.T) {
	t.Parallel()
	in := newExchangeInstance(t)
	in.exchangeStatus = []int{http.StatusServiceUnavailable}
	in.exchangeBody = `unavailable`
	c := retryingClient(t, in).ForOrganization("team-a")
	if _, err := c.CreateProject(t.Context(), ProjectCreate{ProjectName: "svc"}); err != nil {
		t.Fatalf("a create must survive a retryable exchange failure: %v", err)
	}
	if in.exchangeCount("team-a") != 2 || in.creates.Load() != 1 {
		t.Fatalf("exchanges = %d, creates = %d, want 2 and 1", in.exchangeCount("team-a"), in.creates.Load())
	}
}

func TestExchangeDropsExpiresInWhenTheInstanceCapsTheLifetime(t *testing.T) {
	t.Parallel()
	in := newExchangeInstance(t)
	in.exchangeStatus = []int{http.StatusBadRequest}
	in.exchangeBody = `{"error":"invalid_request","error_description":"expires_in 900 exceeds the maximum of 600"}`
	if _, err := in.client(t, nil).ForOrganization("team-a").ListProjects(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(in.forms) != 2 || in.forms[0]["expires_in"] != "900" {
		t.Fatalf("forms = %v, want a first request with expires_in", in.forms)
	}
	if _, has := in.forms[1]["expires_in"]; has {
		t.Fatalf("the second exchange still sent expires_in: %v", in.forms[1])
	}
}

func TestExchangeFailures(t *testing.T) {
	t.Parallel()
	t.Run("endpoint missing", func(t *testing.T) {
		t.Parallel()
		in := newExchangeInstance(t)
		in.exchangeStatus, in.exchangeBody = []int{http.StatusNotFound}, `{"detail":"Not Found"}`
		_, err := in.client(t, nil).ForOrganization("team-a").ListProjects(t.Context())
		var unavailable *EndpointUnavailableError
		if !errors.As(err, &unavailable) || unavailable.MinimumRelease != tokenExchangeMinimumRelease {
			t.Fatalf("err = %v, want the endpoint to be reported missing", err)
		}
		if IsNotFoundError(err) {
			t.Fatal("a missing token endpoint must never read as a resource that is not found")
		}
	})
	t.Run("key without instance:admin", func(t *testing.T) {
		t.Parallel()
		in := newExchangeInstance(t)
		in.exchangeStatus = []int{http.StatusBadRequest}
		in.exchangeBody = `{"error":"invalid_scope","error_description":"subject_token must include instance:admin"}`
		_, err := in.client(t, nil).ForOrganization("team-a").ListProjects(t.Context())
		var adminErr *InstanceAdminRequiredError
		if !errors.As(err, &adminErr) || !strings.Contains(err.Error(), "instance-admin API key") {
			t.Fatalf("err = %v, want InstanceAdminRequiredError", err)
		}
	})
	t.Run("unknown organization", func(t *testing.T) {
		t.Parallel()
		in := newExchangeInstance(t)
		in.exchangeStatus = []int{http.StatusBadRequest}
		in.exchangeBody = `{"error":"invalid_target","error_description":"unknown organization: 'team-a'"}`
		_, err := in.client(t, nil).ForOrganization("team-a").ListProjects(t.Context())
		if !IsInvalidTargetError(err) {
			t.Fatalf("err = %v, want invalid_target", err)
		}
		const want = `organization "team-a" does not exist on this instance; if it was renamed, set the new name`
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err, want)
		}
	})
}
