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

	resource.Test(t, resource.TestCase{
		IsUnitTest:               true,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = "test-token"
}

data "logfire_pagerduty_service" "test" {
  account_subdomain    = "acme"
  pagerduty_service_id = "PABC123"
}
`, server.URL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.logfire_pagerduty_service.test",
						tfjsonpath.New("account_subdomain"),
						knownvalue.StringExact("acme"),
					),
					statecheck.ExpectKnownValue(
						"data.logfire_pagerduty_service.test",
						tfjsonpath.New("region"),
						knownvalue.StringExact("us"),
					),
					statecheck.ExpectKnownValue(
						"data.logfire_pagerduty_service.test",
						tfjsonpath.New("pagerduty_service_id"),
						knownvalue.StringExact("PABC123"),
					),
					statecheck.ExpectKnownValue(
						"data.logfire_pagerduty_service.test",
						tfjsonpath.New("install_id"),
						knownvalue.StringExact("018f9a6a-1234-7890-abcd-ef0123456789"),
					),
					statecheck.ExpectKnownValue(
						"data.logfire_pagerduty_service.test",
						tfjsonpath.New("service_id"),
						knownvalue.StringExact("018f9a6a-4321-7890-abcd-ef0123456789"),
					),
					statecheck.ExpectKnownValue(
						"data.logfire_pagerduty_service.test",
						tfjsonpath.New("service_name"),
						knownvalue.StringExact("Primary On-call"),
					),
				},
			},
			{
				Config: fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = "test-token"
}

data "logfire_pagerduty_service" "test" {
  account_subdomain    = "acme"
  region               = "eu"
  pagerduty_service_id = "PABC123"
}
`, server.URL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.logfire_pagerduty_service.test",
						tfjsonpath.New("region"),
						knownvalue.StringExact("eu"),
					),
				},
			},
		},
	})
}

func TestPagerDutyServiceDataSourceNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"PagerDuty service not found."}`))
	}))
	defer server.Close()

	resource.Test(t, resource.TestCase{
		IsUnitTest:               true,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = "test-token"
}

data "logfire_pagerduty_service" "test" {
  account_subdomain    = "acme"
  pagerduty_service_id = "PMISSING"
}
`, server.URL),
				ExpectError: regexp.MustCompile("Unable to read PagerDuty service"),
			},
		},
	})
}
