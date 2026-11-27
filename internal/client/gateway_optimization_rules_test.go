// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

const busyRuleBody = `{"detail":"The optimization rule changed while this request saved it. Send the request again."}`

// busyRuleClient answers each PUT with the next status. A 409 carries
// retryAfter as its Retry-After header when retryAfter is set.
func busyRuleClient(t *testing.T, retryAfter string, statuses ...int) (*APIClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	c := newStubClient(t, func(req *http.Request) (*http.Response, error) {
		requireRequest(t, req, http.MethodPut, "/api/v1/gateway/optimization-rules/rule-1/")
		status := statuses[min(int(calls.Add(1))-1, len(statuses)-1)]
		if status == http.StatusOK {
			return stubResponse(req, status, `{"id":"rule-1","name":"Concise"}`), nil
		}
		resp := stubResponse(req, status, busyRuleBody)
		if retryAfter != "" {
			resp.Header.Set("Retry-After", retryAfter)
		}
		return resp, nil
	})
	return c, &calls
}

func TestReplaceGatewayOptimizationRuleRetriesBusyConflict(t *testing.T) {
	t.Parallel()
	c, calls := busyRuleClient(t, "0", http.StatusConflict, http.StatusOK)
	out, err := c.ReplaceGatewayOptimizationRule(context.Background(), "rule-1", GatewayOptimizationRuleWrite{Name: "Concise"})
	if err != nil {
		t.Fatalf("a busy 409 followed by a 200 must succeed, got %v", err)
	}
	if out.ID != "rule-1" || calls.Load() != 2 {
		t.Fatalf("out=%v calls=%d, want rule-1 after 2 calls", out, calls.Load())
	}
}

func TestReplaceGatewayOptimizationRuleStopsAfterBusyAttempts(t *testing.T) {
	t.Parallel()
	c, calls := busyRuleClient(t, "0", http.StatusConflict)
	_, err := c.ReplaceGatewayOptimizationRule(context.Background(), "rule-1", GatewayOptimizationRuleWrite{Name: "Concise"})
	if !IsConflictError(err) || calls.Load() != replaceBusyAttempts {
		t.Fatalf("err=%v calls=%d, want a 409 after %d calls", err, calls.Load(), replaceBusyAttempts)
	}
}

func TestReplaceGatewayOptimizationRuleKeepsOtherConflicts(t *testing.T) {
	t.Parallel()
	c, calls := busyRuleClient(t, "", http.StatusConflict, http.StatusOK)
	_, err := c.ReplaceGatewayOptimizationRule(context.Background(), "rule-1", GatewayOptimizationRuleWrite{Name: "Concise"})
	if !IsConflictError(err) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d, want one 409 without a retry", err, calls.Load())
	}
}
