// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rateLimitedThenCreated answers the first refusals POST requests with a public API rate-limit
// refusal and every later request with a created schedule. It returns a retrying client with the
// given timeout and the count of POST requests the server saw.
func rateLimitedThenCreated(t *testing.T, refusals int32, retryAfter, resource string, timeout time.Duration) (*APIClient, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "office hours") {
			t.Errorf("attempt %d body = %q; want the original create body", posts.Load()+1, body)
		}
		if posts.Add(1) <= refusals {
			if resource != "" {
				w.Header().Set("X-RateLimit-Resource", resource)
			}
			w.Header().Set("Retry-After", retryAfter)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"detail":"Rate limit exceeded"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"00000000-0000-0000-0000-000000000001","label":"office hours","timezone":"UTC"}`))
	}))
	t.Cleanup(server.Close)
	client, err := NewAPIClient(server.URL, "test", &http.Client{
		Timeout: timeout,
		Transport: &retryingTransport{
			next:        http.DefaultTransport,
			maxAttempts: defaultRetryMaxAttempts,
			baseDelay:   time.Millisecond,
			maxDelay:    2 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, &posts
}

func TestCreateRetriesPublicAPIRateLimitRefusals(t *testing.T) {
	t.Parallel()
	// More refusals than defaultRetryMaxAttempts: a refusal does not use up the attempt count.
	client, posts := rateLimitedThenCreated(t, defaultRetryMaxAttempts+1, "0", "public_api", 5*time.Second)

	created, err := client.CreateSchedule(context.Background(), ScheduleCreate{Label: "office hours", Timezone: "UTC"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if created.Label != "office hours" {
		t.Fatalf("created label = %q; want office hours", created.Label)
	}
	if got, want := posts.Load(), int32(defaultRetryMaxAttempts+2); got != want {
		t.Fatalf("create requests = %d; want %d", got, want)
	}
}

func TestCreateDoesNotRetryOtherRateLimitResponses(t *testing.T) {
	t.Parallel()
	for name, resource := range map[string]string{"no resource header": "", "another resource": "mcp"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, posts := rateLimitedThenCreated(t, 1, "0", resource, 5*time.Second)

			_, err := client.CreateSchedule(context.Background(), ScheduleCreate{Label: "office hours", Timezone: "UTC"})
			if !IsRateLimitError(err) {
				t.Fatalf("CreateSchedule error = %v; want the 429", err)
			}
			if got := posts.Load(); got != 1 {
				t.Fatalf("create requests = %d; want 1", got)
			}
		})
	}
}

func TestRateLimitRetryStopsBeforeTheClientTimeout(t *testing.T) {
	t.Parallel()
	// Retry-After is longer than the client timeout, so a wait could only end in a timeout error.
	client, posts := rateLimitedThenCreated(t, 1, "3", "public_api", time.Second)

	started := time.Now()
	_, err := client.CreateSchedule(context.Background(), ScheduleCreate{Label: "office hours", Timezone: "UTC"})
	if !IsRateLimitError(err) {
		t.Fatalf("CreateSchedule error = %v; want the 429", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("CreateSchedule returned after %s; want the 429 without a wait", elapsed)
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("create requests = %d; want 1", got)
	}
}

func TestRateLimitRetryFitsBudgetWithoutClientTimeout(t *testing.T) {
	t.Parallel()
	start := time.Now()
	if !retryFitsBudget(context.Background(), start, time.Second) {
		t.Fatal("a one-second wait must fit the budget")
	}
	if retryFitsBudget(context.Background(), start, maxRateLimitRetryWait+time.Second) {
		t.Fatal("a wait past maxRateLimitRetryWait must not fit the budget")
	}
}
