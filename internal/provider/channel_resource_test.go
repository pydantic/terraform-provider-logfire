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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccChannelResource(t *testing.T) {
	t.Parallel()

	channelName := fmt.Sprintf("acc-channel-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	updatedChannelName := fmt.Sprintf("%s-renamed", channelName)

	initialURL := "https://example.com/webhook"
	updatedURL := "https://example.com/webhook/updated"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,

		Steps: []resource.TestStep{
			{
				Config: testAccChannelResourceConfig(channelName, "auto", initialURL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(channelName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("active"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("type"), knownvalue.StringExact("webhook")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("format"), knownvalue.StringExact("auto")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("url"), knownvalue.StringExact(initialURL)),
				},
			},
			{
				ResourceName:      "logfire_channel.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_channel.test"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					id := resourceState.Primary.Attributes["id"]
					return id, nil
				},
			},
			{
				Config: testAccChannelResourceConfig(channelName, "auto", initialURL),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccChannelResourceConfig(updatedChannelName, "slack-blockkit", updatedURL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(updatedChannelName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("format"), knownvalue.StringExact("slack-blockkit")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("url"), knownvalue.StringExact(updatedURL)),
				},
			},
		},
	})
}

func testAccChannelResourceConfig(channelName, format, url string) string {
	return fmt.Sprintf(`%s

resource "logfire_channel" "test" {
  name = %q

  config {
    type   = "webhook"
    format = %q
    url    = %q
  }
}
`, testAccProviderConfig(), channelName, format, url)
}
