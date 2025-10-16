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

	org := "terraform-provider-logfire"
	projectName := fmt.Sprintf("acc-test-channel-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	channelName := fmt.Sprintf("acc-channel-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	updatedChannelName := fmt.Sprintf("%s-renamed", channelName)

	initialURL := "https://example.com/webhook"
	updatedURL := "https://example.com/webhook/updated"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,

		Steps: []resource.TestStep{
			{
				Config: testAccChannelResourceConfig(org, projectName, channelName, "auto", initialURL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("organization"), knownvalue.StringExact(org)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("project"), knownvalue.StringExact(projectName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(channelName)),
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
					org := resourceState.Primary.Attributes["organization"]
					project := resourceState.Primary.Attributes["project"]
					id := resourceState.Primary.Attributes["id"]
					return fmt.Sprintf("%s/%s/%s", org, project, id), nil
				},
			},
			{
				Config: testAccChannelResourceConfig(org, projectName, channelName, "auto", initialURL),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccChannelResourceConfig(org, projectName, updatedChannelName, "slack-blockkit", updatedURL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(updatedChannelName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("format"), knownvalue.StringExact("slack-blockkit")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("url"), knownvalue.StringExact(updatedURL)),
				},
			},
		},
	})
}

func testAccChannelResourceConfig(org, projectName, channelName, format, url string) string {
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  organization = %q
  name         = %q
  description  = "Acceptance test project"
}

resource "logfire_channel" "test" {
  organization = %q
  project      = logfire_project.test.name
  name         = %q

  config {
    type   = "webhook"
    format = %q
    url    = %q
  }
}
`, testAccProviderConfig(), org, projectName, org, channelName, format, url)
}
