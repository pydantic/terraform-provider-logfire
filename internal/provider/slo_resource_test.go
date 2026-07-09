// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Requires an account whose plan includes SLOs (the API is gated).
func TestAccSloResource(t *testing.T) {
	t.Parallel()

	projectName := fmt.Sprintf("acc-test-slo-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	sloName := fmt.Sprintf("acc-slo-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	sloUpdatedName := fmt.Sprintf("%s-renamed", sloName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccSloResourceConfig(projectName, sloName, stringPtr("Initial SLO description"), "99.9", "30d", nil),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("project_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("service_name"), knownvalue.StringExact("payments-api")),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("name"), knownvalue.StringExact(sloName)),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("description"), knownvalue.StringExact("Initial SLO description")),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("source"), knownvalue.StringExact("records")),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("target_percent"), knownvalue.StringExact("99.9")),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("rolling_window"), knownvalue.StringExact("30d")),
				},
			},
			{
				ResourceName:      "logfire_slo.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_slo.test"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					projectID := resourceState.Primary.Attributes["project_id"]
					id := resourceState.Primary.Attributes["id"]
					return fmt.Sprintf("%s/%s", projectID, id), nil
				},
			},
			{
				ResourceName:      "logfire_slo.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					// Import by project name + SLO name
					return fmt.Sprintf("%s/%s", projectName, sloName), nil
				},
			},
			{
				Config: testAccSloResourceConfig(projectName, sloName, stringPtr("Initial SLO description"), "99.9", "30d", nil),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccSloResourceConfig(projectName, sloUpdatedName, stringPtr("Updated SLO description"), "99.95", "14d", []string{"prod"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("name"), knownvalue.StringExact(sloUpdatedName)),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("description"), knownvalue.StringExact("Updated SLO description")),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("target_percent"), knownvalue.StringExact("99.95")),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("rolling_window"), knownvalue.StringExact("14d")),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("environments"), knownvalue.SetSizeExact(1)),
				},
			},
			{
				Config: testAccSloResourceConfig(projectName, sloUpdatedName, nil, "99.95", "14d", nil),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("description"), knownvalue.Null()),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("environments"), knownvalue.Null()),
				},
			},
		},
	})
}

func testAccSloResourceConfig(projectName, sloName string, description *string, targetPercent, rollingWindow string, environments []string) string {
	descriptionLine := ""
	if description != nil {
		descriptionLine = fmt.Sprintf("  description = %q\n", *description)
	}
	environmentsLine := ""
	if environments != nil {
		quoted := ""
		for i, env := range environments {
			if i > 0 {
				quoted += ", "
			}
			quoted += fmt.Sprintf("%q", env)
		}
		environmentsLine = fmt.Sprintf("  environments = [%s]\n", quoted)
	}

	return fmt.Sprintf(`
resource "logfire_project" "test" {
  name = %[1]q
}

resource "logfire_slo" "test" {
  project_id     = logfire_project.test.id
  service_name   = "payments-api"
  name           = %[2]q
%[3]s  total_query    = "parent_span_id IS NULL"
  bad_query      = "otel_status_code = 'ERROR'"
  target_percent = %[4]q
  rolling_window = %[5]q
%[6]s}
`, projectName, sloName, descriptionLine, targetPercent, rollingWindow, environmentsLine)
}
