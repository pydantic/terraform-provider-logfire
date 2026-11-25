// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

// GatewaySpendingLimits holds one limit per window in whole US dollars. A nil
// window has no limit.
type GatewaySpendingLimits struct {
	DailyUSD   *int64 `json:"daily_usd"`
	WeeklyUSD  *int64 `json:"weekly_usd"`
	MonthlyUSD *int64 `json:"monthly_usd"`
}

const (
	gatewayOrganizationSpendingLimitsPath = "/api/v1/gateway/spending-limits/"
	gatewaySpendingLimitsMinimumRelease   = "v2026-10-16.01"
)

// GetGatewayOrganizationSpendingLimits reads the limits of the authenticated
// organization. That route has no 404 of its own, so a 404 means the instance
// predates the Gateway spending limits API.
func (c *APIClient) GetGatewayOrganizationSpendingLimits(ctx context.Context) (*GatewaySpendingLimits, error) {
	out, err := request[GatewaySpendingLimits](ctx, c, http.MethodGet,
		gatewayOrganizationSpendingLimitsPath, nil)
	return out, unavailableIfNotFound(err, http.MethodGet,
		gatewayOrganizationSpendingLimitsPath,
		gatewaySpendingLimitsMinimumRelease, "")
}

// SetGatewayOrganizationSpendingLimits sends every window, so a nil window
// removes that limit.
func (c *APIClient) SetGatewayOrganizationSpendingLimits(ctx context.Context, in GatewaySpendingLimits) (*GatewaySpendingLimits, error) {
	out, err := request[GatewaySpendingLimits](ctx, c, http.MethodPatch,
		gatewayOrganizationSpendingLimitsPath, in)
	return out, unavailableIfNotFound(err, http.MethodPatch,
		gatewayOrganizationSpendingLimitsPath,
		gatewaySpendingLimitsMinimumRelease, "")
}

// GetGatewayProjectSpendingLimits reads the limits of one project.
func (c *APIClient) GetGatewayProjectSpendingLimits(ctx context.Context, projectID string) (*GatewaySpendingLimits, error) {
	return request[GatewaySpendingLimits](ctx, c, http.MethodGet,
		apiPath("gateway", "spending-limits", "projects", projectID), nil)
}

// SetGatewayProjectSpendingLimits sends every window, so a nil window removes
// that limit.
func (c *APIClient) SetGatewayProjectSpendingLimits(ctx context.Context, projectID string, in GatewaySpendingLimits) (*GatewaySpendingLimits, error) {
	return request[GatewaySpendingLimits](ctx, c, http.MethodPatch,
		apiPath("gateway", "spending-limits", "projects", projectID), in)
}

// GatewayProjectSpendingLimitsUnavailableError translates a 404 into an upgrade
// hint. Supported releases answer an unknown or inaccessible project with 403,
// so a 404 means the route is missing. The caller must still confirm that the
// project exists, because the error would otherwise blame the release for a
// deleted project.
func GatewayProjectSpendingLimitsUnavailableError(method string, err error) error {
	return unavailableIfNotFound(err, method,
		gatewayOrganizationSpendingLimitsPath+"projects/{project_id}/",
		gatewaySpendingLimitsMinimumRelease, "")
}
