// Copyright (c) Pydantic, Inc.
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

func TestAccDashboardResource(t *testing.T) {
	t.Parallel()

	projectName := fmt.Sprintf("acc-test-dashboard-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	dashboardSlug := fmt.Sprintf("acc-dashboard-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	dashboardName := fmt.Sprintf("Dashboard %s", dashboardSlug)
	updatedDashboardSlug := fmt.Sprintf("%s-updated", dashboardSlug)
	updatedDashboardName := fmt.Sprintf("%s Updated", dashboardName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDashboardResourceConfig(
					projectName,
					dashboardSlug,
					dashboardName,
					`{"metadata": {"name": "placeholder"}, "widgets": []}`,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_dashboard.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_dashboard.test", tfjsonpath.New("project_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_dashboard.test", tfjsonpath.New("name"), knownvalue.StringExact(dashboardName)),
					statecheck.ExpectKnownValue("logfire_dashboard.test", tfjsonpath.New("slug"), knownvalue.StringExact(dashboardSlug)),
				},
			},
			{
				ResourceName:      "logfire_dashboard.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					// Import by project name + dashboard slug
					return fmt.Sprintf("%s/%s", projectName, dashboardSlug), nil
				},
			},
			{
				Config: testAccDashboardResourceConfig(
					projectName,
					dashboardSlug,
					dashboardName,
					`{"metadata": {"name": "placeholder"}, "widgets": []}`,
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccDashboardResourceConfig(
					projectName,
					updatedDashboardSlug,
					updatedDashboardName,
					`{"metadata": {"name": "placeholder"}, "widgets": [{"kind": "TextWidget", "spec": {"text": "hello"}}]}`,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_dashboard.test", tfjsonpath.New("name"), knownvalue.StringExact(updatedDashboardName)),
					statecheck.ExpectKnownValue("logfire_dashboard.test", tfjsonpath.New("slug"), knownvalue.StringExact(updatedDashboardSlug)),
				},
			},
		},
	})
}

func testAccDashboardResourceConfig(projectName, slug, name, definition string) string {
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name = %q
}

resource "logfire_dashboard" "test" {
  project_id = logfire_project.test.id
  slug       = %q
  name       = %q
  definition = %q
}
`, testAccProviderConfig(), projectName, slug, name, definition)
}
