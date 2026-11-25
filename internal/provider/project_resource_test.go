// Copyright Pydantic, Inc. 2025, 2026
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
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccProjectResource(t *testing.T) {
	t.Parallel()

	const address = "logfire_project.test"
	baseName := fmt.Sprintf("acc-test-project-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	renamedName := fmt.Sprintf("%s-renamed", baseName)
	var expectedOrgCheck knownvalue.Check = knownvalue.NotNull()
	if org := os.Getenv("LOGFIRE_EXPECTED_ORG"); org != "" {
		expectedOrgCheck = knownvalue.StringExact(org)
	}
	expect := func(attribute string, check knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue(address, tfjsonpath.New(attribute), check)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,

		Steps: []resource.TestStep{
			// CREATE + READ
			{
				Config: testAccProjectResourceConfig(baseName, stringPtr("This is a test project"), nil),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("id", knownvalue.NotNull()),
					expect("organization", expectedOrgCheck),
					expect("name", knownvalue.StringExact(baseName)),
					expect("description", knownvalue.StringExact("This is a test project")),
					expect("visibility", knownvalue.StringExact("public")),
				},
			},

			// IMPORT and verify no drift
			{
				ResourceName:      address,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccImportIDFromAttributes(address, "organization", "name"),
			},
			{
				// Re-apply the exact same config and assert a no-op plan
				Config: testAccProjectResourceConfig(baseName, stringPtr("This is a test project"), nil),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},

			// Rename without changing description or visibility.
			{
				Config: testAccProjectResourceConfig(renamedName, stringPtr("This is a test project"), nil),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					expect("name", knownvalue.StringExact(renamedName)),
					expect("description", knownvalue.StringExact("This is a test project")),
					expect("visibility", knownvalue.StringExact("public")),
				},
			},

			// Clearing the description still works after a rename.
			{
				Config: testAccProjectResourceConfig(baseName, nil, nil),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("id", knownvalue.NotNull()),
					expect("organization", expectedOrgCheck),
					expect("name", knownvalue.StringExact(baseName)),
					expect("description", knownvalue.Null()),
					expect("visibility", knownvalue.StringExact("public")),
				},
			},

			{
				Config: testAccProjectResourceConfig(renamedName, nil, stringPtr("private")),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("name", knownvalue.StringExact(renamedName)),
					expect("description", knownvalue.Null()),
					expect("visibility", knownvalue.StringExact("private")),
				},
			},
			// IMPORT via ID to confirm the new format works
			{
				ResourceName:      address,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccImportIDFromAttributes(address, "id"),
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
