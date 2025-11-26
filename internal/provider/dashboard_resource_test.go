// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
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
	initialDefinition := testAccDashboardDefinition("placeholder text")
	updatedDefinition := testAccDashboardDefinition("hello world")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDashboardResourceConfig(
					projectName,
					dashboardSlug,
					dashboardName,
					initialDefinition,
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
					initialDefinition,
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
					updatedDefinition,
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

func testAccDashboardDefinition(widgetText string) string {
	payload := map[string]any{
		"kind":     "Dashboard",
		"metadata": map[string]any{},
		"spec": map[string]any{
			"display": map[string]any{
				"name":        "placeholder",
				"description": "Acceptance test dashboard",
			},
			"panels": map[string]any{
				"panel": map[string]any{
					"kind": "Panel",
					"spec": map[string]any{
						"display": map[string]any{
							"name":        "panel",
							"description": "text panel for acceptance tests",
						},
						"plugin": map[string]any{
							"kind": "TextPanel",
							"spec": map[string]any{
								"text": widgetText,
							},
						},
						"queries": []any{},
					},
				},
			},
			"layouts": []any{
				map[string]any{
					"kind": "Grid",
					"spec": map[string]any{
						"display": map[string]any{
							"title": "Main",
						},
						"items": []any{
							map[string]any{
								"x":      0,
								"y":      0,
								"width":  24,
								"height": 6,
								"content": map[string]any{
									"$ref": "#/spec/panels/panel",
								},
							},
						},
					},
				},
			},
			"variables":       []any{},
			"datasources":     map[string]any{},
			"duration":        "1h",
			"refreshInterval": "0s",
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal dashboard definition: %v", err))
	}
	return string(raw)
}
