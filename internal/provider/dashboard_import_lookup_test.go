// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestDashboardImportLookup(t *testing.T) {
	t.Parallel()
	dashboards := []logclient.DashboardSummary{
		{ID: "other", DashboardSlug: "dashboard-1"},
		{ID: "dashboard-1", DashboardSlug: "alpha"},
		{ID: "dashboard-2", DashboardSlug: "beta"},
		{ID: "dashboard-3", DashboardSlug: "beta"},
	}
	for _, test := range []struct {
		name, key, slug, wantID string
		wantMatches             int
	}{
		{name: "ID takes precedence over slug", key: "dashboard-1", wantID: "dashboard-1"},
		{name: "slug lookup", key: "alpha", wantID: "dashboard-1", wantMatches: 1},
		{name: "provided slug", key: "missing", slug: "alpha", wantID: "dashboard-1", wantMatches: 1},
		{name: "duplicate slugs", key: "beta", wantID: "dashboard-2", wantMatches: 2},
		{name: "missing dashboard", key: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, matches := findDashboardSummaryByIDOrSlug(dashboards, test.key, test.slug)
			if test.wantID == "" {
				if got != nil {
					t.Fatalf("lookup = %#v; want no match", got)
				}
			} else if got == nil || got.ID != test.wantID {
				t.Fatalf("lookup = %#v; want ID %q", got, test.wantID)
			}
			if len(matches) != test.wantMatches {
				t.Fatalf("slug matches = %d; want %d", len(matches), test.wantMatches)
			}
		})
	}
}
