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

func TestAccAlertResourceOmittedDescription(t *testing.T) {
	t.Parallel()

	projectName := fmt.Sprintf("acc-test-alert-no-desc-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	channelPrimaryName := fmt.Sprintf("acc-alert-channel-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	channelSecondaryName := fmt.Sprintf("%s-secondary", channelPrimaryName)
	alertName := fmt.Sprintf("acc-alert-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	config := testAccAlertResourceConfig(
		projectName,
		channelPrimaryName,
		channelSecondaryName,
		alertName,
		nil,
		"select 1",
		"5m",
		"5m",
		"has_matches",
		true,
		false,
		nil,
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("description"), knownvalue.Null()),
				},
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}
