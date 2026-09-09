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
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccGatewayAPIKeyResource(t *testing.T) {
	t.Parallel()

	projectName := fmt.Sprintf("acc-gw-key-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	keyName := fmt.Sprintf("acc-gw-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGatewayAPIKeyResourceConfig(projectName, keyName, "10", "false"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_gateway_api_key.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_gateway_api_key.test", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_gateway_api_key.test", tfjsonpath.New("active"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("logfire_gateway_api_key.test", tfjsonpath.New("spending_limit_daily"), knownvalue.Int64Exact(10)),
				},
			},
			{
				Config: testAccGatewayAPIKeyResourceConfig(projectName, keyName, "10", "false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Spend caps update in place.
				Config: testAccGatewayAPIKeyResourceConfig(projectName, keyName, "25", "true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("logfire_gateway_api_key.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_gateway_api_key.test", tfjsonpath.New("spending_limit_daily"), knownvalue.Int64Exact(25)),
					statecheck.ExpectKnownValue("logfire_gateway_api_key.test", tfjsonpath.New("cache_enabled"), knownvalue.Bool(true)),
				},
			},
			{
				// Import recovers the key without the create-only token.
				ResourceName:      "logfire_gateway_api_key.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"token",
				},
			},
		},
	})
}

func testAccGatewayAPIKeyResourceConfig(projectName, keyName, dailyLimit, cacheEnabled string) string {
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name        = %q
  description = "Acceptance test project for gateway API key"
}

resource "logfire_gateway_api_key" "test" {
  name                 = %q
  project_id           = logfire_project.test.id
  spending_limit_daily = %s
  cache_enabled        = %s
}
`, testAccProviderConfig(), projectName, keyName, dailyLimit, cacheEnabled)
}
