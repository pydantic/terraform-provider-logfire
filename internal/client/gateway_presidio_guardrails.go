// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

const gatewayPresidioGuardrailsPath = "/api/v1/gateway/guardrails/presidio/"

// GatewayPresidioOperator selects how Presidio anonymizes a detected entity.
// The type is "redact" or "replace". NewValue is set only for "replace".
type GatewayPresidioOperator struct {
	Type     string  `json:"type"`
	NewValue *string `json:"new_value,omitempty"`
}

// GatewayPresidioGuardrailRead is the public Presidio guardrail representation.
type GatewayPresidioGuardrailRead struct {
	ID             string                   `json:"id"`
	Name           string                   `json:"name"`
	Description    *string                  `json:"description"`
	ConnectionID   string                   `json:"connection_id"`
	OnError        string                   `json:"on_error"`
	EntityType     string                   `json:"entity_type"`
	Language       string                   `json:"language"`
	Operator       *GatewayPresidioOperator `json:"operator"`
	ScoreThreshold *float64                 `json:"score_threshold"`
	Targets        []GatewayGuardrailTarget `json:"targets"`
	SampleTexts    []string                 `json:"sample_texts"`
	Status         string                   `json:"status"`
	InactiveReason *string                  `json:"inactive_reason"`
	CreatedAt      string                   `json:"created_at"`
	UpdatedAt      string                   `json:"updated_at"`
}

// GatewayPresidioGuardrailWrite is the full desired state of a Presidio guardrail.
// A nil SampleTexts leaves the stored samples unchanged.
type GatewayPresidioGuardrailWrite struct {
	Name           string                   `json:"name"`
	Description    *string                  `json:"description"`
	ConnectionID   string                   `json:"connection_id"`
	OnError        string                   `json:"on_error"`
	EntityType     string                   `json:"entity_type"`
	Language       string                   `json:"language"`
	Operator       *GatewayPresidioOperator `json:"operator"`
	ScoreThreshold *float64                 `json:"score_threshold"`
	Targets        []GatewayGuardrailTarget `json:"targets"`
	SampleTexts    *[]string                `json:"sample_texts,omitempty"`
}

type gatewayPresidioGuardrailsPage struct {
	Guardrails []GatewayPresidioGuardrailRead `json:"guardrails"`
}

// CreateGatewayPresidioGuardrail sends one create attempt because POST is not idempotent.
func (c *APIClient) CreateGatewayPresidioGuardrail(ctx context.Context, in GatewayPresidioGuardrailWrite) (*GatewayPresidioGuardrailRead, error) {
	out, err := create[GatewayPresidioGuardrailRead](ctx, c, gatewayPresidioGuardrailsPath, in, http.StatusCreated)
	return out, unavailableIfNotFound(err, http.MethodPost, gatewayPresidioGuardrailsPath, gatewayGuardrailsMinimumRelease, "")
}

func (c *APIClient) GetGatewayPresidioGuardrail(ctx context.Context, id string) (*GatewayPresidioGuardrailRead, error) {
	return request[GatewayPresidioGuardrailRead](ctx, c, http.MethodGet, apiPath("gateway", "guardrails", "presidio", id), nil)
}

// FindGatewayPresidioGuardrailByName returns the organization's Presidio guardrail with the given name.
func (c *APIClient) FindGatewayPresidioGuardrailByName(ctx context.Context, name string) (*GatewayPresidioGuardrailRead, error) {
	return findGatewayGuardrailByName(ctx, c, gatewayPresidioGuardrailsPath, "Gateway Presidio guardrail", name,
		func(p *gatewayPresidioGuardrailsPage) []GatewayPresidioGuardrailRead { return p.Guardrails })
}

// ReplaceGatewayPresidioGuardrail replaces the Presidio guardrail and its targets.
func (c *APIClient) ReplaceGatewayPresidioGuardrail(ctx context.Context, id string, in GatewayPresidioGuardrailWrite) (*GatewayPresidioGuardrailRead, error) {
	return request[GatewayPresidioGuardrailRead](ctx, c, http.MethodPut, apiPath("gateway", "guardrails", "presidio", id), in)
}

// DeleteGatewayPresidioGuardrail idempotently deletes a Presidio guardrail.
func (c *APIClient) DeleteGatewayPresidioGuardrail(ctx context.Context, id string) error {
	return c.remove(ctx, apiPath("gateway", "guardrails", "presidio", id))
}
