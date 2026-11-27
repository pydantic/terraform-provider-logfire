// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// gatewayOptimizationRulesPath is the rule collection route. It has no 404 of
// its own, so a 404 from it means the instance predates the optimization rules
// API.
const gatewayOptimizationRulesPath = "/api/v1/gateway/optimization-rules/"

const gatewayOptimizationRulesMinimumRelease = "v2026-11-27.01"

// replaceBusyAttempts bounds the PUT attempts when Logfire answers that the
// rule changed while it saved the request.
const replaceBusyAttempts = 3

// GatewayOptimizationTarget selects the Gateway requests that an optimization
// rule applies to. A nil pattern or agent name matches every request on the
// endpoint.
type GatewayOptimizationTarget struct {
	EndpointSlug        string  `json:"endpoint_slug"`
	ModelPattern        *string `json:"model_pattern"`
	AgentName           *string `json:"agent_name"`
	SystemPromptPattern *string `json:"system_prompt_pattern"`
}

// GatewayOptimizationRuleWrite is the complete desired state of a rule. Create
// and replace send the same body.
type GatewayOptimizationRuleWrite struct {
	Name           string                      `json:"name"`
	Description    *string                     `json:"description"`
	Category       string                      `json:"category"`
	Instruction    string                      `json:"instruction"`
	MessageRole    string                      `json:"message_role"`
	TriggerPattern *string                     `json:"trigger_pattern"`
	Priority       int64                       `json:"priority"`
	FailureMode    string                      `json:"failure_mode"`
	Action         string                      `json:"action"`
	Enabled        bool                        `json:"enabled"`
	Targets        []GatewayOptimizationTarget `json:"targets"`
}

// GatewayOptimizationRuleRead is the public optimization rule representation.
type GatewayOptimizationRuleRead struct {
	ID             string                      `json:"id"`
	Name           string                      `json:"name"`
	Description    *string                     `json:"description"`
	Category       string                      `json:"category"`
	Instruction    string                      `json:"instruction"`
	MessageRole    string                      `json:"message_role"`
	TriggerPattern *string                     `json:"trigger_pattern"`
	Priority       int64                       `json:"priority"`
	FailureMode    string                      `json:"failure_mode"`
	Action         string                      `json:"action"`
	Enabled        bool                        `json:"enabled"`
	Source         string                      `json:"source"`
	TemplateSlug   *string                     `json:"template_slug"`
	Targets        []GatewayOptimizationTarget `json:"targets"`
	CreatedAt      string                      `json:"created_at"`
	UpdatedAt      string                      `json:"updated_at"`
}

// GatewayOptimizationRulesPage is one page of the keyset-paginated rule list.
type GatewayOptimizationRulesPage struct {
	Rules      []GatewayOptimizationRuleRead `json:"rules"`
	NextCursor *string                       `json:"next_cursor"`
}

// CreateGatewayOptimizationRule sends one create attempt because POST is not
// idempotent.
func (c *APIClient) CreateGatewayOptimizationRule(ctx context.Context, in GatewayOptimizationRuleWrite) (*GatewayOptimizationRuleRead, error) {
	out, err := create[GatewayOptimizationRuleRead](
		ctx, c, gatewayOptimizationRulesPath, in, http.StatusCreated)
	return out, unavailableIfNotFound(err, http.MethodPost,
		gatewayOptimizationRulesPath, gatewayOptimizationRulesMinimumRelease, "")
}

// GetGatewayOptimizationRule reads a rule of the authenticated organization.
func (c *APIClient) GetGatewayOptimizationRule(ctx context.Context, id string) (*GatewayOptimizationRuleRead, error) {
	return request[GatewayOptimizationRuleRead](
		ctx, c, http.MethodGet, apiPath("gateway", "optimization-rules", id), nil)
}

// FindGatewayOptimizationRuleByName returns the organization's rule with the
// given name. The list filters by the exact name, and a name is unique in an
// organization.
func (c *APIClient) FindGatewayOptimizationRuleByName(ctx context.Context, name string) (*GatewayOptimizationRuleRead, error) {
	query := url.Values{"name": []string{name}, "limit": []string{"1"}}
	page, err := request[GatewayOptimizationRulesPage](ctx, c, http.MethodGet,
		gatewayOptimizationRulesPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil, unavailableIfNotFound(err, http.MethodGet,
			gatewayOptimizationRulesPath, gatewayOptimizationRulesMinimumRelease, "")
	}
	if len(page.Rules) == 0 {
		return nil, &APIError{
			StatusCode: http.StatusNotFound,
			Message:    fmt.Sprintf("Gateway optimization rule named %q not found in the credential's organization", name),
		}
	}
	return &page.Rules[0], nil
}

// ReplaceGatewayOptimizationRule stores the complete desired state of a rule,
// including its targets. Logfire answers 409 with a Retry-After header when a
// concurrent write changed the rule during the save. The same request then
// succeeds, so the client waits and sends it again, a bounded number of times.
// The other 409 answers carry no Retry-After header, and the client does not
// retry them.
func (c *APIClient) ReplaceGatewayOptimizationRule(ctx context.Context, id string, in GatewayOptimizationRuleWrite) (*GatewayOptimizationRuleRead, error) {
	path := apiPath("gateway", "optimization-rules", id)
	for attempt := 1; ; attempt++ {
		out := new(GatewayOptimizationRuleRead)
		resp, err := c.doJSON(ctx, http.MethodPut, path, in, out, http.StatusOK)
		if err == nil {
			return out, nil
		}
		delay, busy := busyRetryAfter(resp, err)
		if !busy || attempt >= replaceBusyAttempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// busyRetryAfter returns the wait of a 409 that says the resource is busy.
// Such an answer carries a Retry-After header in seconds. The wait is at most
// five seconds.
func busyRetryAfter(resp *http.Response, err error) (time.Duration, bool) {
	if !IsConflictError(err) || resp == nil {
		return 0, false
	}
	seconds, parseErr := strconv.Atoi(resp.Header.Get("Retry-After"))
	if parseErr != nil || seconds < 0 {
		return 0, false
	}
	return min(time.Duration(seconds)*time.Second, 5*time.Second), true
}

// DeleteGatewayOptimizationRule idempotently deletes a rule and its targets.
func (c *APIClient) DeleteGatewayOptimizationRule(ctx context.Context, id string) error {
	return c.remove(ctx, apiPath("gateway", "optimization-rules", id))
}
