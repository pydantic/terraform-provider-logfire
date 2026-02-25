// Copyright Pydantic, Inc. 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
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

	orgName := fmt.Sprintf("acc-org-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	displayName := fmt.Sprintf("Acceptance %s", orgName)
	updatedDisplayName := displayName + " Updated"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			testAccOrganizationPreCheck(t)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationResourceConfig(orgName, displayName, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_organization.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_organization.test", tfjsonpath.New("name"), knownvalue.StringExact(orgName)),
					statecheck.ExpectKnownValue("logfire_organization.test", tfjsonpath.New("display_name"), knownvalue.StringExact(displayName)),
					statecheck.ExpectKnownValue("logfire_organization.test", tfjsonpath.New("deletion_protection"), knownvalue.Bool(false)),
				},
			},
			{
				ResourceName:      "logfire_organization.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"deletion_protection",
				},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_organization.test"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					return resourceState.Primary.Attributes["id"], nil
				},
			},
			{
				Config: testAccOrganizationResourceConfig(orgName, displayName, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_organization.test", tfjsonpath.New("deletion_protection"), knownvalue.Bool(false)),
				},
			},
			{
				Config: testAccOrganizationResourceConfig(orgName, updatedDisplayName, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_organization.test", tfjsonpath.New("display_name"), knownvalue.StringExact(updatedDisplayName)),
					statecheck.ExpectKnownValue("logfire_organization.test", tfjsonpath.New("deletion_protection"), knownvalue.Bool(false)),
				},
			},
		},
	})
}

func testAccOrganizationPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("LOGFIRE_TEST_ORGANIZATIONS") == "" {
		t.Skip("set LOGFIRE_TEST_ORGANIZATIONS=1 to run organization acceptance tests")
	}
}

func testAccOrganizationResourceConfig(name, displayName string, deletionProtection bool) string {
	return fmt.Sprintf(`%s

resource "logfire_organization" "test" {
  name                = %q
  display_name        = %q
  deletion_protection = %t
}
`, testAccProviderConfig(), name, displayName, deletionProtection)
}
