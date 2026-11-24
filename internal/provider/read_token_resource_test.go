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
)

func TestAccReadTokenResource(t *testing.T) {
	t.Parallel()
	testAccTokenResource(t, "read")
}

// testAccTokenResource runs the expiry lifecycle shared by the read and write token resources.
func testAccTokenResource(t *testing.T, kind string) {
	address := fmt.Sprintf("logfire_%s_token.test", kind)
	projectName := fmt.Sprintf("acc-%s-token-%s", kind, acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	expiresAt := time.Now().UTC().AddDate(0, 1, 0).Format("2006-01-02") + "T23:59:59Z"
	withExpiry := fmt.Sprintf("%q", expiresAt)
	replace := accPreApply(plancheck.ExpectResourceAction(address, plancheck.ResourceActionReplace))
	empty := accPreApply(plancheck.ExpectEmptyPlan())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTokenResourceConfig(kind, projectName, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "id", knownvalue.NotNull()),
					expectAccValue(address, "project_id", knownvalue.NotNull()),
					expectAccValue(address, "token_prefix", knownvalue.NotNull()),
					expectAccValue(address, "token", knownvalue.NotNull()),
					expectAccValue(address, "expires_at", knownvalue.Null()),
				},
			},
			{
				Config:           testAccTokenResourceConfig(kind, projectName, ""),
				ConfigPlanChecks: empty,
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "token_prefix", knownvalue.NotNull()),
					expectAccValue(address, "token", knownvalue.NotNull()),
					expectAccValue(address, "expires_at", knownvalue.Null()),
				},
			},
			{
				Config:           testAccTokenResourceConfig(kind, projectName, withExpiry),
				ConfigPlanChecks: replace,
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "token_prefix", knownvalue.NotNull()),
					expectAccValue(address, "token", knownvalue.NotNull()),
					expectAccValue(address, "expires_at", knownvalue.StringExact(expiresAt)),
				},
			},
			{
				Config:           testAccTokenResourceConfig(kind, projectName, withExpiry),
				ConfigPlanChecks: empty,
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "expires_at", knownvalue.StringExact(expiresAt)),
				},
			},
			{
				Config:           testAccTokenResourceConfig(kind, projectName, "null"),
				ConfigPlanChecks: replace,
				ConfigStateChecks: []statecheck.StateCheck{
					expectAccValue(address, "expires_at", knownvalue.Null()),
				},
			},
		},
	})
}

// testAccTokenResourceConfig renders a project and its token. expiresAt is an HCL expression,
// and the empty string omits the attribute.
func testAccTokenResourceConfig(kind, projectName, expiresAt string) string {
	expiresAtLine := ""
	if expiresAt != "" {
		expiresAtLine = fmt.Sprintf("  expires_at = %s\n", expiresAt)
	}
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name        = %q
  description = "Acceptance test project for %s token"
}

resource "logfire_%s_token" "test" {
  project_id = logfire_project.test.id
%s}
`, testAccProviderConfig(), projectName, kind, kind, expiresAtLine)
}
