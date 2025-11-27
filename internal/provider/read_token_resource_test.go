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

func TestAccReadTokenResource(t *testing.T) {
	t.Parallel()

	projectName := fmt.Sprintf("acc-read-token-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccReadTokenResourceConfig(projectName),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_read_token.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_read_token.test", tfjsonpath.New("project_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_read_token.test", tfjsonpath.New("token"), knownvalue.NotNull()),
				},
			},
			{
				Config: testAccReadTokenResourceConfig(projectName),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_read_token.test", tfjsonpath.New("token_prefix"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_read_token.test", tfjsonpath.New("token"), knownvalue.NotNull()),
				},
			},
		},
	})
}

func testAccReadTokenResourceConfig(projectName string) string {
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name        = %q
  description = "Acceptance test project for read token"
}

resource "logfire_read_token" "test" {
  project_id = logfire_project.test.id
}
`, testAccProviderConfig(), projectName)
}
