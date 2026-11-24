// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
	"net/url"
)

type PagerDutyService struct {
	InstallID          string `json:"install_id"`
	ServiceID          string `json:"service_id"`
	PagerDutyServiceID string `json:"pagerduty_service_id"`
	ServiceName        string `json:"service_name"`
}

func (c *APIClient) GetPagerDutyService(ctx context.Context, accountSubdomain, region, pagerDutyServiceID string) (*PagerDutyService, error) {
	query := url.Values{
		"account_subdomain": []string{accountSubdomain},
		"region":            []string{region},
	}
	path := apiPath("integrations", "pagerduty", "services",
		pagerDutyServiceID) + "?" + query.Encode()
	return request[PagerDutyService](ctx, c, http.MethodGet, path, nil)
}
