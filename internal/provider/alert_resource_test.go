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

func TestAccAlertResource(t *testing.T) {
	t.Parallel()

	org := "terraform-provider-logfire"
	projectName := fmt.Sprintf("acc-test-alert-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	channelPrimaryName := fmt.Sprintf("acc-alert-channel-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	channelSecondaryName := fmt.Sprintf("%s-secondary", channelPrimaryName)
	alertName := fmt.Sprintf("acc-alert-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	alertUpdatedName := fmt.Sprintf("%s-renamed", alertName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAlertResourceConfig(
					org,
					projectName,
					channelPrimaryName,
					channelSecondaryName,
					alertName,
					"Initial alert description",
					"select 1",
					"5m",
					"5m",
					"has_matches",
					true,
					false,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("organization"), knownvalue.StringExact(org)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("project"), knownvalue.StringExact(projectName)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("name"), knownvalue.StringExact(alertName)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("description"), knownvalue.StringExact("Initial alert description")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("query"), knownvalue.StringExact("select 1")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("time_window"), knownvalue.StringExact("5m")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("frequency"), knownvalue.StringExact("5m")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("watermark"), knownvalue.StringExact("45s")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("notify_when"), knownvalue.StringExact("has_matches")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("active"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("channel_ids"), knownvalue.SetSizeExact(1)),
				},
			},
			{
				ResourceName:      "logfire_alert.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_alert.test"]
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
				Config: testAccAlertResourceConfig(
					org,
					projectName,
					channelPrimaryName,
					channelSecondaryName,
					alertName,
					"Initial alert description",
					"select 1",
					"5m",
					"5m",
					"has_matches",
					true,
					false,
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccAlertResourceConfig(
					org,
					projectName,
					channelPrimaryName,
					channelSecondaryName,
					alertUpdatedName,
					"Updated alert description",
					"select 2",
					"1m",
					"1m",
					"has_matches_changed",
					false,
					true,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("name"), knownvalue.StringExact(alertUpdatedName)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("description"), knownvalue.StringExact("Updated alert description")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("query"), knownvalue.StringExact("select 2")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("time_window"), knownvalue.StringExact("1m")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("frequency"), knownvalue.StringExact("1m")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("watermark"), knownvalue.StringExact("45s")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("notify_when"), knownvalue.StringExact("has_matches_changed")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("active"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("channel_ids"), knownvalue.SetSizeExact(2)),
				},
			},
		},
	})
}

func testAccAlertResourceConfig(org, projectName, channelPrimaryName, channelSecondaryName, alertName, description, query, timeWindow, frequency, notifyWhen string, active bool, includeSecondary bool) string {
	channelIDs := "logfire_channel.primary.id"
	if includeSecondary {
		channelIDs = "logfire_channel.primary.id, logfire_channel.secondary.id"
	}

	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  organization = %q
  name         = %q
  description  = "Acceptance test project"
}

resource "logfire_channel" "primary" {
  organization = %q
  project      = logfire_project.test.name
  name         = %q

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/webhook/primary"
  }
}

resource "logfire_channel" "secondary" {
  organization = %q
  project      = logfire_project.test.name
  name         = %q

  config {
    type   = "webhook"
    format = "raw-data"
    url    = "https://example.com/webhook/secondary"
  }
}

resource "logfire_alert" "test" {
  organization = %q
  project      = logfire_project.test.name
  name         = %q
  description  = %q
  query        = %q
  time_window  = %q
  frequency    = %q
  channel_ids  = [%s]
  notify_when  = %q
  active       = %t
}
`, testAccProviderConfig(), org, projectName, org, channelPrimaryName, org, channelSecondaryName, org, alertName, description, query, timeWindow, frequency, channelIDs, notifyWhen, active)
}
