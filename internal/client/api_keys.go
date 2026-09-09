// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// GatewaySpendingLimitMaxUSD is the maximum spend limit, in whole US dollars,
// allowed for a gateway API key. It mirrors GATEWAY_SPENDING_LIMIT_MAX_USD in
// the backend's scope_claims module.
const GatewaySpendingLimitMaxUSD = 500_000_000

// Project-bound scopes. Keys carrying one of these must be scoped to a single
// project; they cannot be combined with all_projects. Mirrors
// OAuthScope.project_scoped in the backend.
var projectBoundScopes = map[string]struct{}{
	"project:gateway_proxy": {},
	"project:read_otlp":     {},
	"project:write_otlp":    {},
}

// IsProjectBoundScope reports whether the scope requires a specific project_id.
func IsProjectBoundScope(scope string) bool {
	_, ok := projectBoundScopes[scope]
	return ok
}

// GatewayProxyClaims carries the per-scope settings for a
// `project:gateway_proxy` key: spend caps plus the response-cache toggle.
// A nil or omitted limit means no limit for that window; a nil CacheEnabled
// inherits the project default.
type GatewayProxyClaims struct {
	SpendingLimitDaily   *int  `json:"spending_limit_daily,omitempty"`
	SpendingLimitWeekly  *int  `json:"spending_limit_weekly,omitempty"`
	SpendingLimitMonthly *int  `json:"spending_limit_monthly,omitempty"`
	SpendingLimitTotal   *int  `json:"spending_limit_total,omitempty"`
	CacheEnabled         *bool `json:"cache_enabled,omitempty"`
}

// ScopeClaims maps a scope id to that scope's settings. Only
// `project:gateway_proxy` carries settings today.
type ScopeClaims struct {
	GatewayProxy *GatewayProxyClaims `json:"project:gateway_proxy,omitempty"`
}

// APIKeyRead is the plaintext-free representation of an API key returned by
// list and update endpoints. The secret is only ever returned by create (and
// rotate) endpoints.
type APIKeyRead struct {
	ID             string      `json:"id"`
	OrganizationID string      `json:"organization_id"`
	Name           string      `json:"name"`
	Description    *string     `json:"description"`
	Scopes         []string    `json:"scopes"`
	ProjectID      *string     `json:"project_id"`
	ProjectName    *string     `json:"project_name"`
	AllProjects    bool        `json:"all_projects"`
	CreatedBy      *string     `json:"created_by"`
	CreatedByName  *string     `json:"created_by_name"`
	CreatedAt      string      `json:"created_at"`
	LastUsedAt     *string     `json:"last_used_at"`
	ExpiresAt      *string     `json:"expires_at"`
	UserID         *string     `json:"user_id"`
	Active         bool        `json:"active"`
	UpdatedAt      *string     `json:"updated_at"`
	UpdatedBy      *string     `json:"updated_by"`
	Claims         ScopeClaims `json:"claims"`
}

// APIKeyCreate is the request body for creating an API key. A nil ProjectID
// mints an org-wide key; a nil Claims omits per-scope settings.
type APIKeyCreate struct {
	Name        string       `json:"name"`
	Scopes      []string     `json:"scopes"`
	Description *string      `json:"description,omitempty"`
	Claims      *ScopeClaims `json:"claims,omitempty"`
	ProjectID   *string      `json:"project_id,omitempty"`
	ExpiresAt   *string      `json:"expires_at,omitempty"`
}

// APIKeyCreateOutput pairs the created key with its plaintext token, which is
// returned only once at creation.
type APIKeyCreateOutput struct {
	APIKey APIKeyRead `json:"api_key"`
	Token  string     `json:"token"`
}

// APIKeyUpdate patches an API key's display metadata and/or per-scope claims.
// Name and Description use presence semantics: an unset field is left
// unchanged, while an explicitly null Description clears it. Claims is a
// PATCH-merge map: a nil Claims omits claims entirely, a present scope object
// merges per settings field (value sets, null clears).
type APIKeyUpdate struct {
	Name        NullableField[string] `json:"name,omitempty"`
	Description NullableField[string] `json:"description,omitempty"`
	Claims      *map[string]any       `json:"claims,omitempty"`
}

func (c *APIClient) apiKeysBase() string {
	return "/api/v1/api-keys/"
}

func (c *APIClient) apiKeyPath(id string) string {
	return fmt.Sprintf("%s%s/", c.apiKeysBase(), url.PathEscape(id))
}

// CreateAPIKey mints a delegated API key. Creation is not idempotent, so no
// automatic retry is attempted.
func (c *APIClient) CreateAPIKey(ctx context.Context, in APIKeyCreate) (*APIKeyCreateOutput, error) {
	var out APIKeyCreateOutput
	_, err := c.doJSON(disableAutomaticRetries(ctx), http.MethodPost, c.apiKeysBase(), in, &out, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAPIKeys returns the organization's API keys visible to the calling
// credential, including project-scoped keys.
func (c *APIClient) ListAPIKeys(ctx context.Context) ([]APIKeyRead, error) {
	var out []APIKeyRead
	_, err := c.doJSON(ctx, http.MethodGet, c.apiKeysBase(), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetAPIKey returns a single API key by ID. The list endpoint is the only
// read path (there is no GET-by-id route), so this filters client-side and
// reports a 404-style error when the ID is absent.
func (c *APIClient) GetAPIKey(ctx context.Context, id string) (*APIKeyRead, error) {
	keys, err := c.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if keys[i].ID == id {
			return &keys[i], nil
		}
	}
	return nil, &APIError{StatusCode: http.StatusNotFound, Message: "API key not found"}
}

// UpdateAPIKey patches an API key's name, description, and/or claims.
func (c *APIClient) UpdateAPIKey(ctx context.Context, id string, in APIKeyUpdate) (*APIKeyRead, error) {
	var out APIKeyRead
	_, err := c.doJSON(ctx, http.MethodPatch, c.apiKeyPath(id), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAPIKey revokes (soft-deletes) an API key. The key stops
// authenticating immediately.
func (c *APIClient) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, c.apiKeyPath(id), nil, nil, http.StatusNoContent)
	return err
}
