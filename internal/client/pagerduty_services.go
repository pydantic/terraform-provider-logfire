// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

type PagerDutyService struct {
	InstallID          string `json:"install_id"`
	ServiceID          string `json:"service_id"`
	PagerDutyServiceID string `json:"pagerduty_service_id"`
	ServiceName        string `json:"service_name"`
}

func (c *APIClient) GetPagerDutyService(
	ctx context.Context,
	accountSubdomain string,
	region string,
	pagerDutyServiceID string,
) (*PagerDutyService, error) {
	query := url.Values{}
	query.Set("account_subdomain", accountSubdomain)
	query.Set("region", region)
	path := fmt.Sprintf(
		"/api/v1/integrations/pagerduty/services/%s/?%s",
		url.PathEscape(pagerDutyServiceID),
		query.Encode(),
	)

	var out PagerDutyService
	_, err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
