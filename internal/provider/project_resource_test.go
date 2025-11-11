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

func TestAccProjectResource(t *testing.T) {
	t.Parallel()

	baseName := fmt.Sprintf("acc-test-project-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	renamedName := fmt.Sprintf("%s-renamed", baseName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,

		Steps: []resource.TestStep{
			// CREATE + READ
			{
				Config: testAccProjectResourceConfig(baseName, stringPtr("This is a test project"), nil),
				// Before Apply, assert that we expect a create of the resource.
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("organization"), knownvalue.StringExact("terraform-provider-logfire")),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("name"), knownvalue.StringExact(baseName)),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("description"), knownvalue.StringExact("This is a test project")),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("visibility"), knownvalue.StringExact("public")),
				},
			},

			// IMPORT and verify no drift
			{
				ResourceName:      "logfire_project.test",
				ImportState:       true,
				ImportStateVerify: true,

				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_project.test"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					org := resourceState.Primary.Attributes["organization"]
					name := resourceState.Primary.Attributes["name"]
					return fmt.Sprintf("%s/%s", org, name), nil
				},
			},
			{
				// Re-apply the exact same config and assert a no-op plan
				Config: testAccProjectResourceConfig(baseName, stringPtr("This is a test project"), nil),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},

			// UPDATE 1: clear description -> expect Null in state, and Update action
			{
				Config: testAccProjectResourceConfig(baseName, nil, nil),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("organization"), knownvalue.StringExact("terraform-provider-logfire")),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("name"), knownvalue.StringExact(baseName)),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("description"), knownvalue.Null()),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("visibility"), knownvalue.StringExact("public")),
				},
			},

			{
				Config: testAccProjectResourceConfig(renamedName, nil, stringPtr("private")),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("name"), knownvalue.StringExact(renamedName)),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("description"), knownvalue.Null()),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("visibility"), knownvalue.StringExact("private")),
				},
			},
			// IMPORT via ID to confirm the new format works
			{
				ResourceName:      "logfire_project.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_project.test"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					return resourceState.Primary.Attributes["id"], nil
				},
			},
			// DELETE happens automatically and is verified by CheckDestroy
		},
	})
}

func testAccProjectResourceConfig(name string, desc *string, visibility *string) string {
	descLine := ""
	if desc != nil {
		descLine = fmt.Sprintf("  description  = %q\n", *desc)
	}

	visibilityLine := ""
	if visibility != nil {
		visibilityLine = fmt.Sprintf("  visibility   = %q\n", *visibility)
	}

	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name         = %q
%s%s}
`, testAccProviderConfig(), name, descLine, visibilityLine)
}

func stringPtr(s string) *string {
	return &s
}
