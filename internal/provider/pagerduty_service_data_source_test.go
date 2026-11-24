// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func pagerDutyServiceConfig(baseURL, serviceID, extra string) string {
	return fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = "test-token"
}

data "logfire_pagerduty_service" "test" {
  account_subdomain    = "acme"
  pagerduty_service_id = %q
%s}
`, baseURL, serviceID, extra)
}

func expectPagerDutyService(want map[string]string) []statecheck.StateCheck {
	checks := []statecheck.StateCheck{}
	for attribute, value := range want {
		checks = append(checks, statecheck.ExpectKnownValue(
			"data.logfire_pagerduty_service.test", tfjsonpath.New(attribute), knownvalue.StringExact(value),
		))
	}
	return checks
}

func TestPagerDutyServiceDataSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/integrations/pagerduty/services/PABC123/" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		region := r.URL.Query().Get("region")
		if r.URL.Query().Get("account_subdomain") != "acme" || (region != "us" && region != "eu") {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"install_id":"018f9a6a-1234-7890-abcd-ef0123456789",
			"service_id":"018f9a6a-4321-7890-abcd-ef0123456789",
			"pagerduty_service_id":"PABC123",
			"service_name":"Primary On-call"
		}`))
	}))
	defer server.Close()

	runUnitTest(t,
		resource.TestStep{
			Config: pagerDutyServiceConfig(server.URL, "PABC123", ""),
			ConfigStateChecks: expectPagerDutyService(map[string]string{
				"account_subdomain":    "acme",
				"region":               "us",
				"pagerduty_service_id": "PABC123",
				"install_id":           "018f9a6a-1234-7890-abcd-ef0123456789",
				"service_id":           "018f9a6a-4321-7890-abcd-ef0123456789",
				"service_name":         "Primary On-call",
			}),
		},
		resource.TestStep{
			Config:            pagerDutyServiceConfig(server.URL, "PABC123", "  region               = \"eu\"\n"),
			ConfigStateChecks: expectPagerDutyService(map[string]string{"region": "eu"}),
		},
	)
}

func TestPagerDutyServiceDataSourceNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"PagerDuty service not found."}`))
	}))
	defer server.Close()

	runUnitTest(t, resource.TestStep{
		Config:      pagerDutyServiceConfig(server.URL, "PMISSING", ""),
		ExpectError: regexp.MustCompile("Unable to read PagerDuty service"),
	})
}
