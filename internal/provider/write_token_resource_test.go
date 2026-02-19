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
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccWriteTokenResource(t *testing.T) {
	t.Parallel()

	projectName := fmt.Sprintf("acc-write-token-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	expiresAt := "2026-11-30T18:45:00Z"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccWriteTokenResourceConfig(projectName, nil),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("project_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("token_prefix"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("expires_at"), knownvalue.Null()),
				},
			},
			{
				Config: testAccWriteTokenResourceConfig(projectName, nil),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("expires_at"), knownvalue.Null()),
				},
			},
			{
				Config: testAccWriteTokenResourceConfig(projectName, stringPtr(expiresAt)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("logfire_write_token.test", plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("token_prefix"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("expires_at"), knownvalue.StringExact(expiresAt)),
				},
			},
			{
				Config: testAccWriteTokenResourceConfig(projectName, stringPtr(expiresAt)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("expires_at"), knownvalue.StringExact(expiresAt)),
				},
			},
			{
				Config: testAccWriteTokenResourceConfigWithNullExpires(projectName),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("logfire_write_token.test", plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_write_token.test", tfjsonpath.New("expires_at"), knownvalue.Null()),
				},
			},
		},
	})
}

func testAccWriteTokenResourceConfig(projectName string, expiresAt *string) string {
	expiresAtLine := ""
	if expiresAt != nil {
		expiresAtLine = fmt.Sprintf("  expires_at = %q\n", *expiresAt)
	}

	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name        = %q
  description = "Acceptance test project for write token"
}

resource "logfire_write_token" "test" {
  project_id = logfire_project.test.id
%s}
`, testAccProviderConfig(), projectName, expiresAtLine)
}

func testAccWriteTokenResourceConfigWithNullExpires(projectName string) string {
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name        = %q
  description = "Acceptance test project for write token"
}

resource "logfire_write_token" "test" {
  project_id = logfire_project.test.id
  expires_at = null
}
`, testAccProviderConfig(), projectName)
}
