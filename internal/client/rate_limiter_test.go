// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"net/http"
	"runtime"
	"testing"
	"time"
)

func TestNewRateLimiterUsesHourlyBurst(t *testing.T) {
	rl := newRateLimiter(50, 1000)

	if got, want := rl.perMinute.Burst(), 50; got != want {
		t.Fatalf("per-minute burst = %d; want %d", got, want)
	}
	if got, want := rl.perHour.Burst(), 1000; got != want {
		t.Fatalf("per-hour burst = %d; want %d", got, want)
	}
}

func TestRetryDelayHonorsRetryAfterHeader(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{
			"Retry-After": []string{"3"},
		},
	}

	delay := retryDelay(resp, 500*time.Millisecond, 15*time.Second, 0)
	if delay != 3*time.Second {
		t.Fatalf("delay = %v; want 3s", delay)
	}
}

func TestIsNotFoundError(t *testing.T) {
	err := &APIError{StatusCode: http.StatusNotFound}
	if !IsNotFoundError(err) {
		t.Fatalf("expected IsNotFoundError to be true for 404")
	}

	err = &APIError{StatusCode: http.StatusInternalServerError}
	if IsNotFoundError(err) {
		t.Fatalf("expected IsNotFoundError to be false for non-404")
	}
}

func TestRateLimiterIsPerHost(t *testing.T) {
	transport := &rateLimitedTransport{next: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	for _, u := range []string{"http://a.test/x", "http://A.TEST/y", "http://b.test/x"} {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	hostLimitersMu.Lock()
	defer hostLimitersMu.Unlock()
	if _, ok := hostLimiters["A.TEST"]; ok {
		t.Error("a host in another case got its own limiter")
	}
	if hostLimiters["a.test"] == nil || hostLimiters["a.test"] == hostLimiters["b.test"] {
		t.Fatalf("hosts must have separate limiters: %v", hostLimiters)
	}
}

// A provider embedded in a long-lived process can see many hosts, so a limiter must not
// leave goroutines behind.
func TestRateLimiterStartsNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	limiters := make([]*rateLimiter, 128)
	for i := range limiters {
		limiters[i] = newRateLimiter(defaultRequestsPerMinute, defaultRequestsPerHour)
	}
	if grown := runtime.NumGoroutine() - before; grown > 4 {
		t.Fatalf("128 limiters started %d goroutines", grown)
	}
	runtime.KeepAlive(limiters)
}
