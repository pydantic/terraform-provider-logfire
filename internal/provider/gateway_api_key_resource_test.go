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
)

func TestAccGatewayAPIKeyResource(t *testing.T) {
	t.Parallel()

	const address = "logfire_gateway_api_key.test"
	projectID := testAccScopedProjectID(t)
	keyName := fmt.Sprintf("acc-gw-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGatewayAPIKeyResourceConfig(projectID, keyName, "10", "false"),
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "id", knownvalue.NotNull()),
					expectAccValue(address, "token", knownvalue.NotNull()),
					expectAccValue(address, "active", knownvalue.Bool(true)),
					expectAccValue(address, "spending_limit_daily", knownvalue.Int64Exact(10)),
				},
			},
			{
				// Spend caps update in place.
				Config: testAccGatewayAPIKeyResourceConfig(projectID, keyName, "25", "true"),
				ConfigPlanChecks: accPreApply(
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "spending_limit_daily", knownvalue.Int64Exact(25)),
					expectAccValue(address, "cache_enabled", knownvalue.Bool(true)),
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

func testAccGatewayAPIKeyResourceConfig(projectID, keyName, dailyLimit, cacheEnabled string) string {
	return fmt.Sprintf(`%s
%s

resource "logfire_gateway_api_key" "test" {
  provider             = logfire.keys
  name                 = %q
  project_id           = %q
  spending_limit_daily = %s
  cache_enabled        = %s
}
`, testAccProviderConfig(), testAccScopedProviderConfig(), keyName, projectID, dailyLimit, cacheEnabled)
}
