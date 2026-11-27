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

const gatewayGuardrailConnectionsPath = "/api/v1/gateway/guardrail-connections/"

// GatewayGuardrailConnectionAuth is the write form of connection authentication.
// The type is "none" or "bearer". Token is write-only.
type GatewayGuardrailConnectionAuth struct {
	Type  string  `json:"type"`
	Token *string `json:"token,omitempty"`
}

// GatewayGuardrailConnectionAuthRead is the read form of connection authentication. It has no token.
type GatewayGuardrailConnectionAuthRead struct {
	Type string `json:"type"`
}

// GatewayGuardrailConnectionRead is the token-free public guardrail connection representation.
type GatewayGuardrailConnectionRead struct {
	ID             string                             `json:"id"`
	Name           string                             `json:"name"`
	Provider       string                             `json:"provider"`
	BaseURL        string                             `json:"base_url"`
	Auth           GatewayGuardrailConnectionAuthRead `json:"auth"`
	TimeoutMS      int64                              `json:"timeout_ms"`
	Enabled        bool                               `json:"enabled"`
	GuardrailCount int64                              `json:"guardrail_count"`
	CreatedAt      string                             `json:"created_at"`
	UpdatedAt      string                             `json:"updated_at"`
}

// GatewayGuardrailConnectionCreate creates a connection to an external guardrail service.
type GatewayGuardrailConnectionCreate struct {
	Name      string                         `json:"name"`
	Provider  string                         `json:"provider"`
	BaseURL   string                         `json:"base_url"`
	Auth      GatewayGuardrailConnectionAuth `json:"auth"`
	TimeoutMS int64                          `json:"timeout_ms"`
	Enabled   bool                           `json:"enabled"`
}

// GatewayGuardrailConnectionUpdate changes only the fields it sets.
type GatewayGuardrailConnectionUpdate struct {
	Name      *string                         `json:"name,omitempty"`
	BaseURL   *string                         `json:"base_url,omitempty"`
	Auth      *GatewayGuardrailConnectionAuth `json:"auth,omitempty"`
	TimeoutMS *int64                          `json:"timeout_ms,omitempty"`
	Enabled   *bool                           `json:"enabled,omitempty"`
}

type gatewayGuardrailConnectionsPage struct {
	Connections []GatewayGuardrailConnectionRead `json:"connections"`
}

// CreateGatewayGuardrailConnection sends one create attempt because POST is
// not idempotent. Logfire tests an enabled connection first and answers 422 if
// the test fails.
func (c *APIClient) CreateGatewayGuardrailConnection(ctx context.Context, in GatewayGuardrailConnectionCreate) (*GatewayGuardrailConnectionRead, error) {
	out, err := create[GatewayGuardrailConnectionRead](ctx, c, gatewayGuardrailConnectionsPath, in, http.StatusCreated)
	return out, unavailableIfNotFound(redactSecret(err, in.Auth.Token), http.MethodPost, gatewayGuardrailConnectionsPath, gatewayGuardrailsMinimumRelease, "")
}

func (c *APIClient) GetGatewayGuardrailConnection(ctx context.Context, id string) (*GatewayGuardrailConnectionRead, error) {
	return request[GatewayGuardrailConnectionRead](ctx, c, http.MethodGet, apiPath("gateway", "guardrail-connections", id), nil)
}

// FindGatewayGuardrailConnectionByName returns the organization's connection with the given name.
func (c *APIClient) FindGatewayGuardrailConnectionByName(ctx context.Context, name string) (*GatewayGuardrailConnectionRead, error) {
	return findGatewayGuardrailByName(ctx, c, gatewayGuardrailConnectionsPath, "Gateway guardrail connection", name,
		func(p *gatewayGuardrailConnectionsPage) []GatewayGuardrailConnectionRead { return p.Connections })
}

// UpdateGatewayGuardrailConnection patches a connection. Logfire tests an
// enabled connection again when its address, authentication, timeout, or state
// changes.
func (c *APIClient) UpdateGatewayGuardrailConnection(ctx context.Context, id string, in GatewayGuardrailConnectionUpdate) (*GatewayGuardrailConnectionRead, error) {
	out, err := request[GatewayGuardrailConnectionRead](ctx, c, http.MethodPatch, apiPath("gateway", "guardrail-connections", id), in)
	if err != nil && in.Auth != nil {
		err = redactSecret(err, in.Auth.Token)
	}
	return out, err
}

// DeleteGatewayGuardrailConnection idempotently deletes a connection. Logfire
// refuses with a 409 while a guardrail uses the connection.
func (c *APIClient) DeleteGatewayGuardrailConnection(ctx context.Context, id string) error {
	return c.remove(ctx, apiPath("gateway", "guardrail-connections", id))
}

// redactSecret removes a write-only secret from an error. An upstream error
// body can repeat the request, so the secret can appear in the API error text.
func redactSecret(err error, secret *string) error {
	if err == nil || secret == nil || *secret == "" {
		return err
	}
	encoded, _ := json.Marshal(*secret)
	forms := []string{*secret, strings.Trim(string(encoded), `"`)}
	replacer := strings.NewReplacer(forms[0], "<redacted>", forms[1], "<redacted>")
	var apiError *APIError
	if errors.As(err, &apiError) {
		message := replacer.Replace(apiError.Message)
		// The client cuts a long error body before this runs, so the body can
		// end in the first part of the secret.
		if body, cut := strings.CutSuffix(message, truncatedSuffix); cut {
			for _, form := range forms {
				for n := len(form) - 1; n > 0; n-- {
					if strings.HasSuffix(body, form[:n]) {
						body = body[:len(body)-n] + "<redacted>"
						break
					}
				}
			}
			message = body + truncatedSuffix
		}
		return &APIError{StatusCode: apiError.StatusCode, Message: message, BackendVersion: apiError.BackendVersion}
	}
	if text := err.Error(); replacer.Replace(text) != text {
		return errors.New(replacer.Replace(text))
	}
	return err
}
