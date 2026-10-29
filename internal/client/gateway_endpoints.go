// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// GatewayEndpointProvider assigns a provider to an endpoint with its routing settings.
type GatewayEndpointProvider struct {
	ProviderID string  `json:"provider_id"`
	Priority   int64   `json:"priority"`
	Weight     float64 `json:"weight"`
	Active     bool    `json:"active"`
}

// GatewayEndpointRead is the public endpoint representation.
type GatewayEndpointRead struct {
	ID          string                    `json:"id"`
	Slug        string                    `json:"slug"`
	Description *string                   `json:"description"`
	Providers   []GatewayEndpointProvider `json:"providers"`
	CreatedAt   string                    `json:"created_at"`
}

// GatewayEndpointCreate creates an endpoint with its complete provider configuration.
type GatewayEndpointCreate struct {
	Slug        string                    `json:"slug"`
	Description *string                   `json:"description,omitempty"`
	Providers   []GatewayEndpointProvider `json:"providers"`
}

// GatewayEndpointUpdate sets the description; a nil description clears it.
type GatewayEndpointUpdate struct {
	Description *string `json:"description"`
}

type gatewayEndpointProvidersReplace struct {
	Providers []GatewayEndpointProvider `json:"providers"`
}

// GatewayEndpointsPage is one page of the keyset-paginated endpoint list.
type GatewayEndpointsPage struct {
	Endpoints  []GatewayEndpointRead `json:"endpoints"`
	NextCursor *string               `json:"next_cursor"`
}

func gatewayEndpointPath(id string) string {
	return "/api/v1/gateway/endpoints/" + url.PathEscape(id) + "/"
}

// CreateGatewayEndpoint sends one create attempt because POST is not idempotent.
func (c *APIClient) CreateGatewayEndpoint(ctx context.Context, in GatewayEndpointCreate) (*GatewayEndpointRead, error) {
	var out GatewayEndpointRead
	_, err := c.doJSON(
		disableAutomaticRetries(ctx), http.MethodPost, "/api/v1/gateway/endpoints/", in, &out, http.StatusCreated,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetGatewayEndpoint reads an endpoint owned by the authenticated organization.
func (c *APIClient) GetGatewayEndpoint(ctx context.Context, id string) (*GatewayEndpointRead, error) {
	var out GatewayEndpointRead
	if _, err := c.doJSON(ctx, http.MethodGet, gatewayEndpointPath(id), nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindGatewayEndpointBySlug returns the organization's endpoint with the given slug.
func (c *APIClient) FindGatewayEndpointBySlug(ctx context.Context, slug string) (*GatewayEndpointRead, error) {
	query := url.Values{"slug": []string{slug}, "limit": []string{"1"}}
	var page GatewayEndpointsPage
	_, err := c.doJSON(ctx, http.MethodGet, "/api/v1/gateway/endpoints/?"+query.Encode(), nil, &page, http.StatusOK)
	if err != nil {
		return nil, err
	}
	if len(page.Endpoints) == 0 {
		return nil, &APIError{
			StatusCode: http.StatusNotFound,
			Message:    fmt.Sprintf("Gateway endpoint with slug %q not found in the credential's organization", slug),
		}
	}
	return &page.Endpoints[0], nil
}

// UpdateGatewayEndpoint changes the endpoint description.
func (c *APIClient) UpdateGatewayEndpoint(
	ctx context.Context, id string, in GatewayEndpointUpdate,
) (*GatewayEndpointRead, error) {
	var out GatewayEndpointRead
	if _, err := c.doJSON(ctx, http.MethodPatch, gatewayEndpointPath(id), in, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReplaceGatewayEndpointProviders atomically replaces the endpoint's provider configuration.
func (c *APIClient) ReplaceGatewayEndpointProviders(
	ctx context.Context, id string, providers []GatewayEndpointProvider,
) (*GatewayEndpointRead, error) {
	var out GatewayEndpointRead
	_, err := c.doJSON(
		ctx, http.MethodPut, gatewayEndpointPath(id)+"providers/", gatewayEndpointProvidersReplace{providers}, &out,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteGatewayEndpoint idempotently deletes an endpoint.
func (c *APIClient) DeleteGatewayEndpoint(ctx context.Context, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, gatewayEndpointPath(id), nil, nil, http.StatusNoContent)
	return err
}
