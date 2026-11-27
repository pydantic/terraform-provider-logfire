// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const gatewayRegexGuardrailsPath = "/api/v1/gateway/guardrails/regex/"

// GatewayPrebuiltGuardrailRead is a configured prebuilt guardrail. Its identity is the template slug.
type GatewayPrebuiltGuardrailRead struct {
	TemplateSlug   string                          `json:"template_slug"`
	Name           string                          `json:"name"`
	Description    string                          `json:"description"`
	Category       string                          `json:"category"`
	Targets        []GatewayGuardrailTarget        `json:"targets"`
	SampleMessages []GatewayGuardrailSampleMessage `json:"sample_messages"`
}

// GatewayPrebuiltGuardrailSave is the full desired state of a prebuilt guardrail.
// A nil SampleMessages leaves the stored samples unchanged.
type GatewayPrebuiltGuardrailSave struct {
	Targets        []GatewayGuardrailTarget         `json:"targets"`
	SampleMessages *[]GatewayGuardrailSampleMessage `json:"sample_messages,omitempty"`
}

// GatewayRegexGuardrailRead is the public custom regex guardrail representation.
type GatewayRegexGuardrailRead struct {
	ID             string                          `json:"id"`
	Name           string                          `json:"name"`
	Description    *string                         `json:"description"`
	Pattern        string                          `json:"pattern"`
	Flags          string                          `json:"flags"`
	Targets        []GatewayGuardrailTarget        `json:"targets"`
	SampleMessages []GatewayGuardrailSampleMessage `json:"sample_messages"`
	CreatedAt      string                          `json:"created_at"`
	UpdatedAt      string                          `json:"updated_at"`
}

// GatewayRegexGuardrailWrite is the full desired state of a regex guardrail.
// A nil SampleMessages leaves the stored samples unchanged.
type GatewayRegexGuardrailWrite struct {
	Name           string                           `json:"name"`
	Description    *string                          `json:"description"`
	Pattern        string                           `json:"pattern"`
	Flags          string                           `json:"flags"`
	Targets        []GatewayGuardrailTarget         `json:"targets"`
	SampleMessages *[]GatewayGuardrailSampleMessage `json:"sample_messages,omitempty"`
}

type gatewayRegexGuardrailsPage struct {
	Guardrails []GatewayRegexGuardrailRead `json:"guardrails"`
}

// GetGatewayPrebuiltGuardrail reads a configured prebuilt guardrail. A 404
// means that the template is unknown or that the organization has not configured it.
func (c *APIClient) GetGatewayPrebuiltGuardrail(ctx context.Context, templateSlug string) (*GatewayPrebuiltGuardrailRead, error) {
	return request[GatewayPrebuiltGuardrailRead](ctx, c, http.MethodGet, apiPath("gateway", "guardrails", "prebuilt", templateSlug), nil)
}

// SaveGatewayPrebuiltGuardrail creates or replaces the configuration of a
// prebuilt guardrail. The PUT is idempotent, so the client can retry it.
func (c *APIClient) SaveGatewayPrebuiltGuardrail(ctx context.Context, templateSlug string, in GatewayPrebuiltGuardrailSave) (*GatewayPrebuiltGuardrailRead, error) {
	out, err := request[GatewayPrebuiltGuardrailRead](ctx, c, http.MethodPut, apiPath("gateway", "guardrails", "prebuilt", templateSlug), in)
	// The route answers an unknown template with its own detail, while FastAPI
	// answers a missing route with "Not Found" and a proxy may send no detail.
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		var body struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal([]byte(apiErr.Message), &body)
		if body.Detail == "" || body.Detail == "Not Found" {
			return nil, unavailableIfNotFound(err, http.MethodPut, "/api/v1/gateway/guardrails/prebuilt/{template_slug}/", gatewayGuardrailsMinimumRelease, "")
		}
	}
	return out, err
}

// DeleteGatewayPrebuiltGuardrail removes the configuration of a prebuilt guardrail.
// Logfire answers 204 when the template is not configured, and 404 when the
// template is unknown.
func (c *APIClient) DeleteGatewayPrebuiltGuardrail(ctx context.Context, templateSlug string) error {
	return c.remove(ctx, apiPath("gateway", "guardrails", "prebuilt", templateSlug))
}

// CreateGatewayRegexGuardrail sends one create attempt because POST is not idempotent.
func (c *APIClient) CreateGatewayRegexGuardrail(ctx context.Context, in GatewayRegexGuardrailWrite) (*GatewayRegexGuardrailRead, error) {
	out, err := create[GatewayRegexGuardrailRead](ctx, c, gatewayRegexGuardrailsPath, in, http.StatusCreated)
	return out, unavailableIfNotFound(regexPatternCheckError(err), http.MethodPost, gatewayRegexGuardrailsPath, gatewayGuardrailsMinimumRelease, "")
}

func (c *APIClient) GetGatewayRegexGuardrail(ctx context.Context, id string) (*GatewayRegexGuardrailRead, error) {
	return request[GatewayRegexGuardrailRead](ctx, c, http.MethodGet, apiPath("gateway", "guardrails", "regex", id), nil)
}

// FindGatewayRegexGuardrailByName returns the organization's regex guardrail with the given name.
func (c *APIClient) FindGatewayRegexGuardrailByName(ctx context.Context, name string) (*GatewayRegexGuardrailRead, error) {
	return findGatewayGuardrailByName(ctx, c, gatewayRegexGuardrailsPath, "Gateway regex guardrail", name,
		func(p *gatewayRegexGuardrailsPage) []GatewayRegexGuardrailRead { return p.Guardrails })
}

// ReplaceGatewayRegexGuardrail replaces the regex guardrail and its targets.
func (c *APIClient) ReplaceGatewayRegexGuardrail(ctx context.Context, id string, in GatewayRegexGuardrailWrite) (*GatewayRegexGuardrailRead, error) {
	out, err := request[GatewayRegexGuardrailRead](ctx, c, http.MethodPut, apiPath("gateway", "guardrails", "regex", id), in)
	return out, regexPatternCheckError(err)
}

// DeleteGatewayRegexGuardrail idempotently deletes a regex guardrail.
func (c *APIClient) DeleteGatewayRegexGuardrail(ctx context.Context, id string) error {
	return c.remove(ctx, apiPath("gateway", "guardrails", "regex", id))
}

// regexPatternCheckError explains the 503 answer of a regex guardrail write that
// the Gateway could not check. Logfire stored nothing, so the next apply can retry it.
func regexPatternCheckError(err error) error {
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusServiceUnavailable || !strings.Contains(apiError.Message, "The pattern could not be checked") {
		return err
	}
	return &APIError{
		StatusCode:     apiError.StatusCode,
		Message:        apiError.Message + ". The Gateway could not check the pattern, so Logfire stored nothing. Apply again after the Retry-After delay",
		BackendVersion: apiError.BackendVersion,
	}
}
