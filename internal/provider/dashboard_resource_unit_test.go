// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
	"testing"
)

func TestFindDashboardSummaryByIDOrSlug(t *testing.T) {
	dashboards := []logclient.DashboardSummary{
		{ID: "dash-1", DashboardSlug: "alpha"},
		{ID: "dash-2", DashboardSlug: "beta"},
		{ID: "dash-3", DashboardSlug: "beta"},
	}

	got, slugMatches := findDashboardSummaryByIDOrSlug(dashboards, "dash-1", "")
	if got == nil || got.ID != "dash-1" {
		t.Fatalf("findDashboardSummaryByIDOrSlug() ID match = %#v, want dash-1", got)
	}
	if len(slugMatches) != 0 {
		t.Fatalf("findDashboardSummaryByIDOrSlug() slugMatches = %d, want 0", len(slugMatches))
	}

	got, slugMatches = findDashboardSummaryByIDOrSlug(dashboards, "beta", "beta")
	if got == nil || got.ID != "dash-2" {
		t.Fatalf("findDashboardSummaryByIDOrSlug() slug match = %#v, want dash-2", got)
	}
	if len(slugMatches) != 2 {
		t.Fatalf("findDashboardSummaryByIDOrSlug() slugMatches = %d, want 2", len(slugMatches))
	}

	got, slugMatches = findDashboardSummaryByIDOrSlug(dashboards, "missing", "")
	if got != nil {
		t.Fatalf("findDashboardSummaryByIDOrSlug() missing match = %#v, want nil", got)
	}
	if slugMatches != nil {
		t.Fatalf("findDashboardSummaryByIDOrSlug() missing slugMatches = %#v, want nil", slugMatches)
	}
}
