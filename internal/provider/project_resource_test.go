// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
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
				Config: testAccProjectResourceConfig(baseName, "This is a test project"),
				// Before Apply, assert that we expect a create of the resource.
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("organization"), knownvalue.StringExact("terraform-provider-logfire")),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("name"), knownvalue.StringExact(baseName)),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("description"), knownvalue.StringExact("This is a test project")),
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
				Config: testAccProjectResourceConfig(baseName, "This is a test project"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},

			// UPDATE 1: clear description -> expect Null in state, and Update action
			{
				Config: testAccProjectResourceConfig(baseName, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("organization"), knownvalue.StringExact("terraform-provider-logfire")),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("name"), knownvalue.StringExact(baseName)),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("description"), knownvalue.Null()),
				},
			},

			{
				Config: testAccProjectResourceConfig(renamedName, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("name"), knownvalue.StringExact(renamedName)),
					statecheck.ExpectKnownValue("logfire_project.test", tfjsonpath.New("description"), knownvalue.Null()),
				},
			},
			// DELETE happens automatically and is verified by CheckDestroy
		},
	})
}

// --- helpers ---

// testAccProviderConfig builds the provider stanza from env.
func testAccProviderConfig() string {
	base := os.Getenv("LOGFIRE_BASE_URL")
	key := os.Getenv("LOGFIRE_API_KEY")
	return fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = %q
}
`, base, key)
}

// testAccProjectResourceConfig builds the resource config.
// If desc is nil, we render description = null to test clearing values.
func testAccProjectResourceConfig(name, desc string) string {
	descLine := ""
	if desc != "" {
		descLine = fmt.Sprintf("  description  = %q\n", desc)
	}

	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  organization = "terraform-provider-logfire"
  name         = %q
%s}
`, testAccProviderConfig(), name, descLine)
}
