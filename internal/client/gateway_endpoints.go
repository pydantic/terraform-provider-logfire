// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// GatewayEndpointProvider assigns a provider to an endpoint with its routing
// settings.
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

// GatewayEndpointCreate creates an endpoint with its complete provider
// configuration.
type GatewayEndpointCreate struct {
	Slug        string                    `json:"slug"`
	Description *string                   `json:"description,omitempty"`
	Providers   []GatewayEndpointProvider `json:"providers"`
}

// GatewayEndpointUpdate sets the description; a nil description clears it.
type GatewayEndpointUpdate struct {
	Description *string `json:"description"`
}

// GatewayEndpointsPage is one page of the keyset-paginated endpoint list.
type GatewayEndpointsPage struct {
	Endpoints  []GatewayEndpointRead `json:"endpoints"`
	NextCursor *string               `json:"next_cursor"`
}

// gatewayEndpointsPath is the endpoint collection route. It has no 404 of its
// own, so a 404 from it means the instance predates the Gateway endpoints API.
const gatewayEndpointsPath = "/api/v1/gateway/endpoints/"

const gatewayEndpointsMinimumRelease = "v2026-10-29.02"

// CreateGatewayEndpoint sends one create attempt because POST is not
// idempotent.
func (c *APIClient) CreateGatewayEndpoint(ctx context.Context, in GatewayEndpointCreate) (*GatewayEndpointRead, error) {
	out, err := create[GatewayEndpointRead](
		ctx, c, gatewayEndpointsPath, in, http.StatusCreated)
	return out, unavailableIfNotFound(err, http.MethodPost,
		gatewayEndpointsPath, gatewayEndpointsMinimumRelease, "")
}

func (c *APIClient) GetGatewayEndpoint(ctx context.Context, id string) (*GatewayEndpointRead, error) {
	return request[GatewayEndpointRead](
		ctx, c, http.MethodGet, apiPath("gateway", "endpoints", id), nil)
}

// FindGatewayEndpointBySlug returns the organization's endpoint with the given
// slug.
func (c *APIClient) FindGatewayEndpointBySlug(ctx context.Context, slug string) (*GatewayEndpointRead, error) {
	query := url.Values{"slug": []string{slug}, "limit": []string{"1"}}
	page, err := request[GatewayEndpointsPage](ctx, c, http.MethodGet,
		gatewayEndpointsPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil, unavailableIfNotFound(err, http.MethodGet,
			gatewayEndpointsPath, gatewayEndpointsMinimumRelease, "")
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
func (c *APIClient) UpdateGatewayEndpoint(ctx context.Context, id string, in GatewayEndpointUpdate) (*GatewayEndpointRead, error) {
	return request[GatewayEndpointRead](
		ctx, c, http.MethodPatch, apiPath("gateway", "endpoints", id), in)
}

// ReplaceGatewayEndpointProviders atomically replaces the endpoint's provider
// configuration.
func (c *APIClient) ReplaceGatewayEndpointProviders(ctx context.Context, id string, providers []GatewayEndpointProvider) (*GatewayEndpointRead, error) {
	return request[GatewayEndpointRead](ctx, c, http.MethodPut,
		apiPath("gateway", "endpoints", id, "providers"),
		map[string][]GatewayEndpointProvider{"providers": providers})
}

// DeleteGatewayEndpoint idempotently deletes an endpoint.
func (c *APIClient) DeleteGatewayEndpoint(ctx context.Context, id string) error {
	return c.remove(ctx, apiPath("gateway", "endpoints", id))
}
