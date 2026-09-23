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

// TestAccSloDelivery covers the delivery configurations of the per-tier
// design: the same channels on every tier through a local, different channels
// per tier, a schedule shared between an SLO and a normal alert, and a
// configuration that manages only some tiers. The second step changes only the
// channels of an existing SLO, which earlier provider releases accepted and
// never sent.
func TestAccSloDelivery(t *testing.T) {
	t.Parallel()

	suffix := acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum)
	projectName := fmt.Sprintf("acc-test-slo-delivery-%s", suffix)
	sameOnEveryTier := `
  alerts = {
    fast   = { channel_assignments = local.everyone }
    medium = { channel_assignments = local.everyone }
    slow   = { channel_assignments = local.everyone }
  }
`
	perTier := `
  alerts = {
    fast   = { channel_assignments = local.oncall }
    medium = { channel_assignments = [{ channel_id = logfire_channel.alerts.id }] }
    slow = {
      channel_assignments = [
        { channel_id = logfire_channel.reliability.id, schedule_id = logfire_schedule.office_hours.id },
      ]
    }
  }
`
	onlyFast := `
  alerts = {
    fast = { channel_assignments = local.oncall }
  }
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccSloDeliveryConfig(projectName, suffix, sameOnEveryTier),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("fast").AtMapKey("channel_assignments"), knownvalue.SetSizeExact(1)),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("medium").AtMapKey("alert_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("slow").AtMapKey("severity"), knownvalue.StringExact("ticket")),
				},
			},
			{
				// Channel-only change on an existing SLO, with a schedule
				// shared with a normal alert.
				Config: testAccSloDeliveryConfig(projectName, suffix, perTier) + testAccSloDeliveryAlertConfig(suffix),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("logfire_slo.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("fast").AtMapKey("channel_assignments"), knownvalue.SetSizeExact(2)),
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("medium").AtMapKey("channel_assignments"), knownvalue.SetSizeExact(1)),
					statecheck.ExpectKnownValue("logfire_alert.shared", tfjsonpath.New("channel_assignments"), knownvalue.SetSizeExact(2)),
				},
			},
			{
				Config: testAccSloDeliveryConfig(projectName, suffix, perTier) + testAccSloDeliveryAlertConfig(suffix),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Tiers that are no longer configured are not managed: their
				// channels stay and the plan is empty.
				Config: testAccSloDeliveryConfig(projectName, suffix, onlyFast) + testAccSloDeliveryAlertConfig(suffix),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("medium").AtMapKey("channel_assignments"), knownvalue.SetSizeExact(1)),
				},
			},
			{
				ResourceName:      "logfire_schedule.office_hours",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccSloDeliveryConfig(projectName, suffix, channels string) string {
	return fmt.Sprintf(`
resource "logfire_project" "test" {
  name = %[1]q
}

resource "logfire_channel" "alerts" {
  name = "acc-slo-alerts-%[2]s"
  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/webhook/alerts"
  }
}

resource "logfire_channel" "pagerduty" {
  name = "acc-slo-pagerduty-%[2]s"
  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/webhook/pagerduty"
  }
}

resource "logfire_channel" "reliability" {
  name = "acc-slo-reliability-%[2]s"
  config {
    type   = "webhook"
    format = "auto"
    url    = "https://example.com/webhook/reliability"
  }
}

resource "logfire_schedule" "office_hours" {
  label    = "acc-office-hours-%[2]s"
  timezone = "Europe/London"
  windows = [
    { days = [1, 2, 3, 4, 5], start_time = "09:00", end_time = "18:00" },
  ]
}

locals {
  everyone = [{ channel_id = logfire_channel.alerts.id }]
  oncall = [
    { channel_id = logfire_channel.pagerduty.id },
    { channel_id = logfire_channel.alerts.id, schedule_id = logfire_schedule.office_hours.id },
  ]
}

resource "logfire_slo" "test" {
  project_id     = logfire_project.test.id
  scope_value    = "payments-api"
  name           = "acc-slo-delivery-%[2]s"
  total_query    = "parent_span_id IS NULL"
  bad_query      = "otel_status_code = 'ERROR'"
  target_percent = "99.9"
  rolling_window = "30d"
%[3]s}
`, projectName, suffix, channels)
}

func testAccSloDeliveryAlertConfig(suffix string) string {
	return fmt.Sprintf(`
resource "logfire_alert" "shared" {
  project_id          = logfire_project.test.id
  name                = "acc-shared-delivery-%s"
  query               = "select 1"
  time_window         = "5m"
  frequency           = "5m"
  channel_assignments = local.oncall
  notify_when         = "has_matches"
}
`, suffix)
}
