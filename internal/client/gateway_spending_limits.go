// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
	"net/url"
)

// GatewaySpendingLimits holds one limit per window in whole US dollars. A nil window has no limit.
type GatewaySpendingLimits struct {
	DailyUSD   *int64 `json:"daily_usd"`
	WeeklyUSD  *int64 `json:"weekly_usd"`
	MonthlyUSD *int64 `json:"monthly_usd"`
}

const gatewayOrganizationSpendingLimitsPath = "/api/v1/gateway/spending-limits/"

func gatewayProjectSpendingLimitsPath(projectID string) string {
	return gatewayOrganizationSpendingLimitsPath + "projects/" + url.PathEscape(projectID) + "/"
}

// GetGatewayOrganizationSpendingLimits reads the limits of the authenticated organization.
func (c *APIClient) GetGatewayOrganizationSpendingLimits(ctx context.Context) (*GatewaySpendingLimits, error) {
	var out GatewaySpendingLimits
	_, err := c.doJSON(ctx, http.MethodGet, gatewayOrganizationSpendingLimitsPath, nil, &out, http.StatusOK)
	if err != nil {
		return nil, gatewaySpendingLimitsUnavailableError(http.MethodGet, err)
	}
	return &out, nil
}

// SetGatewayOrganizationSpendingLimits sends every window, so a nil window removes that limit.
func (c *APIClient) SetGatewayOrganizationSpendingLimits(
	ctx context.Context, in GatewaySpendingLimits,
) (*GatewaySpendingLimits, error) {
	var out GatewaySpendingLimits
	_, err := c.doJSON(ctx, http.MethodPatch, gatewayOrganizationSpendingLimitsPath, in, &out, http.StatusOK)
	if err != nil {
		return nil, gatewaySpendingLimitsUnavailableError(http.MethodPatch, err)
	}
	return &out, nil
}

// GetGatewayProjectSpendingLimits reads the limits of one project.
func (c *APIClient) GetGatewayProjectSpendingLimits(
	ctx context.Context, projectID string,
) (*GatewaySpendingLimits, error) {
	var out GatewaySpendingLimits
	_, err := c.doJSON(ctx, http.MethodGet, gatewayProjectSpendingLimitsPath(projectID), nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetGatewayProjectSpendingLimits sends every window, so a nil window removes that limit.
func (c *APIClient) SetGatewayProjectSpendingLimits(
	ctx context.Context, projectID string, in GatewaySpendingLimits,
) (*GatewaySpendingLimits, error) {
	var out GatewaySpendingLimits
	_, err := c.doJSON(ctx, http.MethodPatch, gatewayProjectSpendingLimitsPath(projectID), in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GatewayProjectSpendingLimitsUnavailableError translates a 404 into an upgrade
// hint. The caller must first confirm that the project exists, because the
// project route also returns 404 for an unknown project.
func GatewayProjectSpendingLimitsUnavailableError(method string, err error) error {
	if !IsNotFoundError(err) {
		return err
	}
	return &EndpointUnavailableError{
		Method:         method,
		Path:           gatewayOrganizationSpendingLimitsPath + "projects/{project_id}/",
		MinimumRelease: gatewaySpendingLimitsMinimumRelease,
		BackendVersion: BackendVersionFromError(err),
	}
}

const gatewaySpendingLimitsMinimumRelease = "v2026-10-16.01"

// gatewaySpendingLimitsUnavailableError translates a 404 from the organization
// route into an upgrade hint. That route has no 404 of its own, so a 404 means
// the instance predates the Gateway spending limits API.
func gatewaySpendingLimitsUnavailableError(method string, err error) error {
	if !IsNotFoundError(err) {
		return err
	}
	return &EndpointUnavailableError{
		Method:         method,
		Path:           gatewayOrganizationSpendingLimitsPath,
		MinimumRelease: gatewaySpendingLimitsMinimumRelease,
		BackendVersion: BackendVersionFromError(err),
	}
}
