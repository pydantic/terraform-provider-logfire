// Copyright Pydantic, Inc. 2025
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
					projectName,
					channelPrimaryName,
					channelSecondaryName,
					alertName,
					stringPtr("Initial alert description"),
					"select 1",
					"5m",
					"5m",
					"has_matches",
					true,
					false,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("project_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("name"), knownvalue.StringExact(alertName)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("description"), knownvalue.StringExact("Initial alert description")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("query"), knownvalue.StringExact("select 1")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("time_window"), knownvalue.StringExact("5m")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("frequency"), knownvalue.StringExact("5m")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("watermark"), knownvalue.StringExact("10s")),
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
					projectID := resourceState.Primary.Attributes["project_id"]
					id := resourceState.Primary.Attributes["id"]
					return fmt.Sprintf("%s/%s", projectID, id), nil
				},
			},
			{
				ResourceName:      "logfire_alert.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					// Import by project name + alert name
					return fmt.Sprintf("%s/%s", projectName, alertName), nil
				},
			},
			{
				Config: testAccAlertResourceConfig(
					projectName,
					channelPrimaryName,
					channelSecondaryName,
					alertName,
					stringPtr("Initial alert description"),
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
					projectName,
					channelPrimaryName,
					channelSecondaryName,
					alertUpdatedName,
					stringPtr("Updated alert description"),
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
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("watermark"), knownvalue.StringExact("10s")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("notify_when"), knownvalue.StringExact("has_matches_changed")),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("active"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New("channel_ids"), knownvalue.SetSizeExact(2)),
				},
			},
		},
	})
}

func testAccAlertResourceConfig(projectName, channelPrimaryName, channelSecondaryName, alertName string, description *string, query, timeWindow, frequency, notifyWhen string, active bool, includeSecondary bool) string {
	channelIDs := "logfire_channel.primary.id"
	if includeSecondary {
		channelIDs = "logfire_channel.primary.id, logfire_channel.secondary.id"
	}

	descLine := ""
	if description != nil {
		descLine = fmt.Sprintf("  description = %q\n", *description)
	}

	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name         = %q
  description  = "Acceptance test project"
}

resource "logfire_channel" "primary" {
  name = %q

  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/webhook/primary"
  }
}

resource "logfire_channel" "secondary" {
  name = %q

  config {
    type   = "webhook"
    format = "raw-data"
    url    = "https://example.com/webhook/secondary"
  }
}

resource "logfire_alert" "test" {
  project_id  = logfire_project.test.id
  name        = %q
%s  query       = %q
  time_window = %q
  frequency   = %q
  channel_ids = [%s]
  notify_when = %q
  active      = %t
}
`, testAccProviderConfig(), projectName, channelPrimaryName, channelSecondaryName, alertName, descLine, query, timeWindow, frequency, channelIDs, notifyWhen, active)
}
