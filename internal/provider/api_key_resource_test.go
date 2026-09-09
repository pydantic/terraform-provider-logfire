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

func TestAccAPIKeyResource(t *testing.T) {
	t.Parallel()

	projectID := testAccScopedProjectID(t)
	keyName := fmt.Sprintf("acc-key-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	expiresAt := "2026-12-31T23:59:59Z"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAPIKeyResourceConfig(projectID, keyName, nil, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("all_projects"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("active"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("expires_at"), knownvalue.Null()),
				},
			},
			{
				Config: testAccAPIKeyResourceConfig(projectID, keyName, nil, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("token"), knownvalue.NotNull()),
				},
			},
			{
				// Name and description update in place.
				Config: testAccAPIKeyResourceConfig(projectID, keyName+"-renamed", nil, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("logfire_api_key.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("name"), knownvalue.StringExact(keyName+"-renamed")),
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("description"), knownvalue.StringExact("acceptance test key")),
				},
			},
			{
				// Expiry change replaces the key.
				Config: testAccAPIKeyResourceConfigWithExpiry(projectID, keyName+"-renamed", expiresAt),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("logfire_api_key.test", plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_api_key.test", tfjsonpath.New("expires_at"), knownvalue.StringExact(expiresAt)),
				},
			},
			{
				// Import recovers the key without the create-only token.
				ResourceName:      "logfire_api_key.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"token",
				},
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

func testAccAPIKeyResourceConfigWithExpiry(projectID, keyName, expiresAt string) string {
	return testAccAPIKeyResourceConfig(projectID, keyName, &expiresAt, true)
}
