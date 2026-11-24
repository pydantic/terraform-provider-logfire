// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// GatewayProviderCredentials contains a write-only upstream API key or service
// account key.
type GatewayProviderCredentials struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// GatewayProviderConfig contains credentials and the optional Bedrock, Azure,
// or Vertex endpoint selection.
type GatewayProviderConfig struct {
	Credentials *GatewayProviderCredentials `json:"credentials,omitempty"`
	API         string                      `json:"api,omitempty"`
	Region      string                      `json:"region,omitempty"`
	Endpoint    string                      `json:"endpoint,omitempty"`
}

// GatewayProviderVendor identifies the upstream vendor and its configuration.
type GatewayProviderVendor struct {
	Vendor string                 `json:"vendor"`
	Config *GatewayProviderConfig `json:"config,omitempty"`
}

// GatewayProviderPricing controls whether requests require known pricing.
type GatewayProviderPricing struct {
	Required bool `json:"required"`
}

// GatewayProviderRead is the credential-free public provider representation.
type GatewayProviderRead struct {
	ID        string                 `json:"id"`
	Slug      string                 `json:"slug"`
	Provider  GatewayProviderVendor  `json:"provider"`
	Pricing   GatewayProviderPricing `json:"pricing"`
	CreatedAt string                 `json:"created_at"`
}

// GatewayProviderCreate creates an organization-owned provider.
type GatewayProviderCreate struct {
	Slug     string                 `json:"slug"`
	Provider GatewayProviderVendor  `json:"provider"`
	Pricing  GatewayProviderPricing `json:"pricing"`
}

// GatewayProviderUpdate changes credentials, pricing, or endpoint settings.
type GatewayProviderUpdate struct {
	Provider *GatewayProviderVendor `json:"provider,omitempty"`
	Pricing  GatewayProviderPricing `json:"pricing"`
}

const gatewayProvidersPath = "/api/v1/gateway/providers/"

// CreateGatewayProvider sends one create attempt because POST is not
// idempotent.
func (c *APIClient) CreateGatewayProvider(ctx context.Context, in GatewayProviderCreate) (*GatewayProviderRead, error) {
	out, err := create[GatewayProviderRead](
		ctx, c, gatewayProvidersPath, in, http.StatusCreated)
	return out, gatewayProviderError(err)
}

func (c *APIClient) GetGatewayProvider(ctx context.Context, id string) (*GatewayProviderRead, error) {
	out, err := request[GatewayProviderRead](
		ctx, c, http.MethodGet, apiPath("gateway", "providers", id), nil)
	return out, gatewayProviderError(err)
}

// GatewayProvidersPage is one page of the slug-ordered keyset-paginated
// provider list.
type GatewayProvidersPage struct {
	Providers  []GatewayProviderRead `json:"providers"`
	NextCursor *string               `json:"next_cursor"`
}

// ListGatewayProviders returns every provider owned by the authenticated
// organization, following the list endpoint's keyset pagination. A 404 means
// the instance predates the Gateway provider management API, because on the
// public API this route has no 404 of its own (unknown ids live under /{id}/).
// It maps to an upgrade hint rather than a bare API error.
func (c *APIClient) ListGatewayProviders(ctx context.Context) ([]GatewayProviderRead, error) {
	var providers []GatewayProviderRead
	cursor := ""
	for {
		var page GatewayProvidersPage
		_, err := c.doJSON(ctx, http.MethodGet,
			pagePath(gatewayProvidersPath, cursor), nil, &page, http.StatusOK)
		if err != nil {
			return nil, unavailableIfNotFound(gatewayProviderError(err),
				http.MethodGet, gatewayProvidersPath,
				"v2026-08-12.01", "logfire-0.13.40")
		}
		providers = append(providers, page.Providers...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return providers, nil
		}
		cursor = *page.NextCursor
	}
}

// FindGatewayProviderBySlug lists the organization's providers and returns the
// one whose slug matches. It mirrors the name-based imports of projects and
// organizations so provider imports do not require knowing the UUID upfront.
func (c *APIClient) FindGatewayProviderBySlug(ctx context.Context, slug string) (*GatewayProviderRead, error) {
	providers, err := c.ListGatewayProviders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range providers {
		if providers[i].Slug == slug {
			return &providers[i], nil
		}
	}
	return nil, &APIError{
		StatusCode: http.StatusNotFound,
		Message: fmt.Sprintf("Gateway provider with slug %q not found in the "+
			"credential's organization", slug),
	}
}

// UpdateGatewayProvider patches a provider without changing its slug or vendor.
func (c *APIClient) UpdateGatewayProvider(ctx context.Context, id string, in GatewayProviderUpdate) (*GatewayProviderRead, error) {
	out, err := request[GatewayProviderRead](
		ctx, c, http.MethodPatch, apiPath("gateway", "providers", id), in)
	return out, gatewayProviderError(err)
}

// DeleteGatewayProvider idempotently deletes a provider and refreshes Gateway
// configuration.
func (c *APIClient) DeleteGatewayProvider(ctx context.Context, id string) error {
	return gatewayProviderError(
		c.remove(ctx, apiPath("gateway", "providers", id)))
}

// gatewayProviderError drops the body of an upstream error, which may echo
// credentials from the request.
func gatewayProviderError(err error) error {
	var apiError *APIError
	if errors.As(err, &apiError) {
		return &APIError{
			StatusCode:     apiError.StatusCode,
			BackendVersion: apiError.BackendVersion,
		}
	}
	return err
}
