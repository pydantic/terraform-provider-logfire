// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func expectAccValue(address, attribute string, check knownvalue.Check) statecheck.StateCheck {
	return statecheck.ExpectKnownValue(address, tfjsonpath.New(attribute), check)
}

func accPreApply(check plancheck.PlanCheck) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{check}}
}

func TestAccAPIKeyResource(t *testing.T) {
	t.Parallel()

	const address = "logfire_api_key.test"
	projectID := testAccScopedProjectID(t)
	keyName := fmt.Sprintf("acc-key-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	expiresAt := time.Now().UTC().AddDate(0, 1, 0).Format("2006-01-02") + "T23:59:59Z"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAPIKeyResourceConfig(projectID, keyName, nil, false),
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "id", knownvalue.NotNull()),
					expectAccValue(address, "token", knownvalue.NotNull()),
					expectAccValue(address, "all_projects", knownvalue.Bool(false)),
					expectAccValue(address, "active", knownvalue.Bool(true)),
					expectAccValue(address, "expires_at", knownvalue.Null()),
				},
			},
			{
				Config:           testAccAPIKeyResourceConfig(projectID, keyName, nil, false),
				ConfigPlanChecks: accPreApply(plancheck.ExpectEmptyPlan()),
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "token", knownvalue.NotNull()),
				},
			},
			{
				// Name and description update in place.
				Config: testAccAPIKeyResourceConfig(projectID, keyName+"-renamed", nil, true),
				ConfigPlanChecks: accPreApply(
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "name", knownvalue.StringExact(keyName+"-renamed")),
					expectAccValue(address, "description", knownvalue.StringExact("acceptance test key")),
				},
			},
			{
				// Expiry change replaces the key.
				Config: testAccAPIKeyResourceConfig(projectID, keyName+"-renamed", &expiresAt, true),
				ConfigPlanChecks: accPreApply(
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionReplace),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "token", knownvalue.NotNull()),
					expectAccValue(address, "expires_at", knownvalue.StringExact(expiresAt)),
				},
			},
			{
				// Import recovers the key without the create-only token.
				ResourceName:            address,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token"},
			},
		},
	})
}

func testAccAPIKeyResourceConfig(projectID, keyName string, expiresAt *string, withDescription bool) string {
	expiresAtLine := ""
	if expiresAt != nil {
		expiresAtLine = fmt.Sprintf("  expires_at  = %q\n", *expiresAt)
	}
	descriptionLine := ""
	if withDescription {
		descriptionLine = "  description = \"acceptance test key\"\n"
	}
	return fmt.Sprintf(`%s
%s

resource "logfire_api_key" "test" {
  provider    = logfire.keys
  name        = %q
  scopes      = ["project:read_otlp", "project:write_otlp"]
  project_id  = %q
%s%s}
`, testAccProviderConfig(), testAccScopedProviderConfig(), keyName, projectID, descriptionLine, expiresAtLine)
}
