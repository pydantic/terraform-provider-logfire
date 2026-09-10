// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// GatewayProviderCredentials contains a write-only upstream API key.
type GatewayProviderCredentials struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// GatewayProviderConfig contains credentials and optional Bedrock endpoint selection.
type GatewayProviderConfig struct {
	Credentials *GatewayProviderCredentials `json:"credentials,omitempty"`
	API         string                      `json:"api,omitempty"`
	Region      string                      `json:"region,omitempty"`
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

// GatewayProviderUpdate changes credentials, pricing, or Bedrock endpoint settings.
type GatewayProviderUpdate struct {
	Provider *GatewayProviderVendor `json:"provider,omitempty"`
	Pricing  GatewayProviderPricing `json:"pricing"`
}

// CreateGatewayProvider sends one create attempt because POST is not idempotent.
func (c *APIClient) CreateGatewayProvider(ctx context.Context, in GatewayProviderCreate) (*GatewayProviderRead, error) {
	var out GatewayProviderRead
	_, err := c.doJSON(
		disableAutomaticRetries(ctx), http.MethodPost, "/api/v1/gateway/providers/", in, &out, http.StatusCreated,
	)
	if err != nil {
		return nil, gatewayProviderError(err)
	}
	return &out, nil
}

// GetGatewayProvider reads a provider owned by the authenticated organization.
func (c *APIClient) GetGatewayProvider(ctx context.Context, id string) (*GatewayProviderRead, error) {
	var out GatewayProviderRead
	_, err := c.doJSON(ctx, http.MethodGet, "/api/v1/gateway/providers/"+url.PathEscape(id)+"/", nil, &out, http.StatusOK)
	if err != nil {
		return nil, gatewayProviderError(err)
	}
	return &out, nil
}

// GatewayProvidersPage is one page of the slug-ordered keyset-paginated provider list.
type GatewayProvidersPage struct {
	Providers  []GatewayProviderRead `json:"providers"`
	NextCursor *string               `json:"next_cursor"`
}

// ListGatewayProviders returns every provider owned by the authenticated
// organization, following the list endpoint's keyset pagination.
func (c *APIClient) ListGatewayProviders(ctx context.Context) ([]GatewayProviderRead, error) {
	var providers []GatewayProviderRead
	cursor := ""
	for {
		query := url.Values{"limit": []string{"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var page GatewayProvidersPage
		_, err := c.doJSON(
			ctx, http.MethodGet, "/api/v1/gateway/providers/?"+query.Encode(), nil, &page, http.StatusOK,
		)
		if err != nil {
			return nil, gatewayProviderError(err)
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
		Message:    fmt.Sprintf("Gateway provider with slug %q not found in the credential's organization", slug),
	}
}

// UpdateGatewayProvider patches a provider without changing its slug or vendor.
func (c *APIClient) UpdateGatewayProvider(
	ctx context.Context, id string, in GatewayProviderUpdate,
) (*GatewayProviderRead, error) {
	var out GatewayProviderRead
	_, err := c.doJSON(ctx, http.MethodPatch, "/api/v1/gateway/providers/"+url.PathEscape(id)+"/", in, &out, http.StatusOK)
	if err != nil {
		return nil, gatewayProviderError(err)
	}
	return &out, nil
}

// DeleteGatewayProvider idempotently deletes a provider and refreshes Gateway configuration.
func (c *APIClient) DeleteGatewayProvider(ctx context.Context, id string) error {
	_, err := c.doJSON(
		ctx, http.MethodDelete, "/api/v1/gateway/providers/"+url.PathEscape(id)+"/", nil, nil, http.StatusNoContent,
	)
	return gatewayProviderError(err)
}

func gatewayProviderError(err error) error {
	var apiError *APIError
	if errors.As(err, &apiError) {
		// An upstream error body may echo credentials from the request.
		return &APIError{StatusCode: apiError.StatusCode}
	}
	return err
}
