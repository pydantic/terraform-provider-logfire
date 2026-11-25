// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccOrganizationResource(t *testing.T) {
	t.Parallel()

	const address = "logfire_organization.test"
	orgName := fmt.Sprintf("acc-org-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	displayName := fmt.Sprintf("Acceptance %s", orgName)
	updatedDisplayName := displayName + " Updated"
	expect := func(attribute string, check knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue(address, tfjsonpath.New(attribute), check)
	}

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if os.Getenv("LOGFIRE_TEST_ORGANIZATIONS") == "" {
				t.Skip("set LOGFIRE_TEST_ORGANIZATIONS=1 to run organization acceptance tests")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationResourceConfig(orgName, displayName),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("id", knownvalue.NotNull()),
					expect("name", knownvalue.StringExact(orgName)),
					expect("display_name", knownvalue.StringExact(displayName)),
					expect("deletion_protection", knownvalue.Bool(false)),
				},
			},
			{
				ResourceName:            address,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"deletion_protection"},
				ImportStateIdFunc:       testAccImportIDFromAttributes(address, "id"),
			},
			{
				Config: testAccOrganizationResourceConfig(orgName, displayName),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("deletion_protection", knownvalue.Bool(false)),
				},
			},
			{
				Config: testAccOrganizationResourceConfig(orgName, updatedDisplayName),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("display_name", knownvalue.StringExact(updatedDisplayName)),
					expect("deletion_protection", knownvalue.Bool(false)),
				},
			},
		},
	})
}

// testAccImportIDFromAttributes joins the named attributes of the resource at
// address with a slash.
func testAccImportIDFromAttributes(address string, attributes ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		resourceState, ok := s.RootModule().Resources[address]
		if !ok || resourceState.Primary == nil {
			return "", fmt.Errorf("resource state not found in root module")
		}
		values := make([]string, len(attributes))
		for i, attribute := range attributes {
			values[i] = resourceState.Primary.Attributes[attribute]
		}
		return strings.Join(values, "/"), nil
	}
}

func testAccOrganizationResourceConfig(name, displayName string) string {
	return fmt.Sprintf(`%s

resource "logfire_organization" "test" {
  name                = %q
  display_name        = %q
  deletion_protection = false
}
`, testAccProviderConfig(), name, displayName)
}
