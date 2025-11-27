// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"net/http"
	"testing"
	"time"
)

func TestNewRateLimiterUsesHourlyBurst(t *testing.T) {
	rl := newRateLimiter(50, 1000)
	t.Cleanup(rl.Close)

	if got, want := cap(rl.perMinute.tokens), 50; got != want {
		t.Fatalf("per-minute burst = %d; want %d", got, want)
	}
	if got, want := cap(rl.perHour.tokens), 1000; got != want {
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
