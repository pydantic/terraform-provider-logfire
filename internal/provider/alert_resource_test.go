// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccRandName(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
}

func TestAccAlertResource(t *testing.T) {
	t.Parallel()

	projectName := testAccRandName("acc-test-alert")
	channelName := testAccRandName("acc-alert-channel")
	alertName := testAccRandName("acc-alert")
	alertUpdatedName := fmt.Sprintf("%s-renamed", alertName)

	initialConfig := testAccAlertResourceConfig(
		projectName, channelName, alertName, stringPtr("Initial alert description"),
		"select 1", "5m", "has_matches", true, false, nil,
	)
	updatedConfig := func(environments []string) string {
		return testAccAlertResourceConfig(
			projectName, channelName, alertUpdatedName, stringPtr("Updated alert description"),
			"select 2", "1m", "has_matches_changed", false, true, environments,
		)
	}
	expect := func(name string, want knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("logfire_alert.test", tfjsonpath.New(name), want)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: initialConfig,
				ConfigStateChecks: []statecheck.StateCheck{
					expect("id", knownvalue.NotNull()),
					expect("project_id", knownvalue.NotNull()),
					expect("name", knownvalue.StringExact(alertName)),
					expect("description", knownvalue.StringExact("Initial alert description")),
					expect("query", knownvalue.StringExact("select 1")),
					expect("time_window", knownvalue.StringExact("5m")),
					expect("frequency", knownvalue.StringExact("5m")),
					expect("watermark", knownvalue.StringExact("10s")),
					expect("notify_when", knownvalue.StringExact("has_matches")),
					expect("active", knownvalue.Bool(true)),
					expect("channel_assignments", knownvalue.SetSizeExact(1)),
					expect("environments", knownvalue.Null()),
				},
			},
			{
				ResourceName:      "logfire_alert.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: testAccImportID("logfire_alert.test", "project_id", "id"),
			},
			{
				ResourceName:      "logfire_alert.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return fmt.Sprintf("%s/%s", projectName, alertName), nil
				},
			},
			{
				Config: initialConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: updatedConfig([]string{"production", "staging"}),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("name", knownvalue.StringExact(alertUpdatedName)),
					expect("description", knownvalue.StringExact("Updated alert description")),
					expect("query", knownvalue.StringExact("select 2")),
					expect("time_window", knownvalue.StringExact("1m")),
					expect("frequency", knownvalue.StringExact("1m")),
					expect("watermark", knownvalue.StringExact("10s")),
					expect("notify_when", knownvalue.StringExact("has_matches_changed")),
					expect("active", knownvalue.Bool(false)),
					expect("channel_assignments", knownvalue.SetSizeExact(2)),
					expect("environments", knownvalue.SetExact([]knownvalue.Check{
						knownvalue.StringExact("production"),
						knownvalue.StringExact("staging"),
					})),
				},
			},
			{
				Config: updatedConfig(nil),
				ConfigStateChecks: []statecheck.StateCheck{
					expect("environments", knownvalue.Null()),
				},
			},
		},
	})
}

func testAccAlertResourceConfig(projectName, channelName, alertName string, description *string, query, window, notifyWhen string, active, includeSecondary bool, environments []string) string {
	channelIDs := "{ channel_id = logfire_channel.primary.id }"
	if includeSecondary {
		channelIDs = "{ channel_id = logfire_channel.primary.id }, { channel_id = logfire_channel.secondary.id }"
	}

	descLine := ""
	if description != nil {
		descLine = fmt.Sprintf("  description = %q\n", *description)
	}

	envLine := ""
	if environments != nil {
		quoted := make([]string, 0, len(environments))
		for _, env := range environments {
			quoted = append(quoted, fmt.Sprintf("%q", env))
		}
		envLine = fmt.Sprintf("  environments = [%s]\n", strings.Join(quoted, ", "))
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
%s  channel_assignments = [%s]
  notify_when = %q
  active      = %t
}
`, testAccProviderConfig(), projectName, channelName, channelName+"-secondary", alertName, descLine, query, window, window, envLine, channelIDs, notifyWhen, active)
}
