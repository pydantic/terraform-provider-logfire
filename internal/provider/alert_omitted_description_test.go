// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccAlertResourceOmittedDescription(t *testing.T) {
	t.Parallel()

	config := testAccAlertResourceConfig(
		testAccRandName("acc-test-alert-no-desc"),
		testAccRandName("acc-alert-channel"),
		testAccRandName("acc-alert"),
		nil, "select 1", "5m", "has_matches", true, false, nil,
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
		},
	})
}
