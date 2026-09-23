// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestSloDeliveryApplied(t *testing.T) {
	t.Parallel()

	sent := &logclient.SloAlertsDelivery{
		Fast: &logclient.SloTierDelivery{ChannelAssignments: []logclient.ChannelAssignment{testPagerduty, testIncidents}},
		Slow: &logclient.SloTierDelivery{ChannelAssignments: []logclient.ChannelAssignment{}},
	}
	for _, tt := range []struct {
		name      string
		sent      *logclient.SloAlertsDelivery
		got       *logclient.SloTierAlerts
		wantTiers string
	}{
		{"nothing sent", nil, nil, ""},
		{
			"every sent tier applied, in another order, with an unsent tier that differs",
			sent,
			sloReadWithTiers([]logclient.ChannelAssignment{testIncidents, testPagerduty}, []logclient.ChannelAssignment{testReliability}, nil).Alerts,
			"",
		},
		{"no alerts in the response", sent, nil, "does not return per-tier `alerts`"},
		{
			"alerts in the response without the sent channels",
			sent,
			sloReadWithTiers(nil, nil, nil).Alerts,
			"the `fast` tier alert",
		},
		{
			"a different schedule",
			sent,
			sloReadWithTiers([]logclient.ChannelAssignment{testPagerduty, {ChannelID: "incidents"}}, nil, nil).Alerts,
			"the `fast` tier alert",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			diags := sloDeliveryApplied(tt.sent, tt.got)
			if tt.wantTiers == "" {
				if diags.HasError() {
					t.Fatalf("unexpected diagnostics: %v", diags)
				}
				return
			}
			if !diags.HasError() {
				t.Fatal("expected an error")
			}
			detail := diags[0].Detail()
			if !strings.Contains(detail, tt.wantTiers) || !strings.Contains(detail, sloDeliveryRelease) {
				t.Fatalf("detail = %q", detail)
			}
		})
	}
}

// fakeSloLogfire serves the SLO routes as a Logfire release of the given
// behaviour does:
//   - "no-alerts": SLO responses have no `alerts` (before tier alerts).
//   - "ignores-alerts": responses list the tier alerts, but writes ignore
//     `alerts`, so every alert keeps no channels (v2026-09-22.02).
//   - "applies-alerts": writes set the named tiers' channels.
func fakeSloLogfire(t *testing.T, behaviour string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var slo map[string]any
	channels := map[string]any{}
	for _, tier := range sloTiers {
		channels[tier] = []any{}
	}
	respond := func(w http.ResponseWriter, status int) {
		body := map[string]any{}
		for k, v := range slo {
			body[k] = v
		}
		if behaviour != "no-alerts" {
			alerts := map[string]any{}
			for _, tier := range sloTiers {
				severity := "page"
				if tier == "slow" {
					severity = "ticket"
				}
				alerts[tier] = map[string]any{"alert_id": "alert-" + tier, "severity": severity, "viable": true, "channel_assignments": channels[tier]}
			}
			body["alerts"] = alerts
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		const collection = "/api/v1/projects/proj-1/slos/"
		var in map[string]any
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			raw, _ := io.ReadAll(r.Body)
			var delivery struct {
				Alerts map[string]struct {
					ChannelAssignments []any `json:"channel_assignments"`
				} `json:"alerts"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Errorf("request body: %v", err)
			}
			if err := json.Unmarshal(raw, &delivery); err != nil {
				t.Errorf("request alerts: %v", err)
			}
			if behaviour == "applies-alerts" {
				for tier, d := range delivery.Alerts {
					channels[tier] = d.ChannelAssignments
				}
			}
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == collection:
			slo = map[string]any{
				"id": "slo-1", "project_id": "proj-1", "scope_kind": "service", "scope_value": in["scope_value"],
				"name": in["name"], "description": nil, "source": "records", "metric_aggregation": "additive",
				"total_query": in["total_query"], "bad_query": in["bad_query"], "threshold": nil, "comparison": nil,
				"target_percent": "99.9000", "rolling_window": "P30D", "environments": []string{},
				"created_at": "2026-09-22T00:00:00Z", "updated_at": nil, "predicate_version": 1,
			}
			respond(w, http.StatusCreated)
		case (r.Method == http.MethodGet || r.Method == http.MethodPatch) && r.URL.Path == collection+"slo-1/" && slo != nil:
			respond(w, http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == collection+"slo-1/":
			slo = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

const testSloFastChannels = `
  alerts = {
    fast = { channel_assignments = [{ channel_id = "pagerduty" }] }
  }
`

func testSloDeliveryReleaseConfig(baseURL, alerts string) string {
	return fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = "test-token"
}

resource "logfire_slo" "test" {
  project_id     = "proj-1"
  scope_value    = "payments-api"
  name           = "payments"
  total_query    = "parent_span_id IS NULL"
  bad_query      = "otel_status_code = 'ERROR'"
  target_percent = "99.9"
  rolling_window = "30d"
%s}
`, baseURL, alerts)
}

// TestSloDeliveryOnOlderLogfire applies SLO channels against Logfire releases
// that do not set them, on create and on an update that adds them. The apply
// must fail, instead of saving the SLO with alerts that notify no channel.
func TestSloDeliveryOnOlderLogfire(t *testing.T) {
	tooOld := regexp.MustCompile(`Logfire release too old for SLO channel assignments`)
	for _, behaviour := range []string{"no-alerts", "ignores-alerts"} {
		t.Run(behaviour+"/create", func(t *testing.T) {
			server := fakeSloLogfire(t, behaviour)
			defer server.Close()
			resource.Test(t, resource.TestCase{
				IsUnitTest:               true,
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testSloDeliveryReleaseConfig(server.URL, testSloFastChannels),
					ExpectError: tooOld,
				}},
			})
		})
		t.Run(behaviour+"/update", func(t *testing.T) {
			server := fakeSloLogfire(t, behaviour)
			defer server.Close()
			resource.Test(t, resource.TestCase{
				IsUnitTest:               true,
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: testSloDeliveryReleaseConfig(server.URL, "")},
					{Config: testSloDeliveryReleaseConfig(server.URL, testSloFastChannels), ExpectError: tooOld},
				},
			})
		})
	}

	t.Run("applies-alerts", func(t *testing.T) {
		server := fakeSloLogfire(t, "applies-alerts")
		defer server.Close()
		resource.Test(t, resource.TestCase{
			IsUnitTest:               true,
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: testSloDeliveryReleaseConfig(server.URL, testSloFastChannels),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("fast").AtMapKey("channel_assignments"), knownvalue.SetSizeExact(1)),
				},
			}},
		})
	})
}

// TestSloUnconfiguredTiersAreNotManaged checks that a tier, or the whole
// `alerts`, that is removed from the configuration plans no change and keeps
// its channels.
func TestSloUnconfiguredTiersAreNotManaged(t *testing.T) {
	server := fakeSloLogfire(t, "applies-alerts")
	defer server.Close()
	every := `
  alerts = {
    fast   = { channel_assignments = [{ channel_id = "pagerduty" }] }
    medium = { channel_assignments = [{ channel_id = "incidents" }] }
    slow   = { channel_assignments = [{ channel_id = "reliability" }] }
  }
`
	fastWithoutAssignments := `
  alerts = {
    fast = {}
  }
`
	resource.Test(t, resource.TestCase{
		IsUnitTest:               true,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: testSloDeliveryReleaseConfig(server.URL, every)},
			{
				Config: testSloDeliveryReleaseConfig(server.URL, testSloFastChannels),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("slow").AtMapKey("channel_assignments"), knownvalue.SetSizeExact(1)),
				},
			},
			{
				// A tier object without channel_assignments also leaves its
				// current channels unmanaged.
				Config: testSloDeliveryReleaseConfig(server.URL, fastWithoutAssignments),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_slo.test", tfjsonpath.New("alerts").AtMapKey("fast").AtMapKey("channel_assignments"), knownvalue.SetSizeExact(1)),
				},
			},
			{
				Config: testSloDeliveryReleaseConfig(server.URL, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: testSloDeliveryReleaseConfig(server.URL, "  alerts = null\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestSloConfiguredUnknownAssignmentsAreNotTakenFromState checks an update
// whose complete assignment set comes from another resource. It is unknown
// during planning, but configured: ModifyPlan must preserve it so Terraform can
// resolve and apply it, rather than substituting the old channels from state.
func TestSloConfiguredUnknownAssignmentsAreNotTakenFromState(t *testing.T) {
	for _, tt := range []struct {
		name       string
		alerts     string
		dependency string
	}{
		{
			name: "assignment set",
			alerts: `
  alerts = {
    fast = { channel_assignments = toset(terraform_data.assignments.output) }
  }
			`,
			dependency: `
resource "terraform_data" "assignments" {
  input = [{ channel_id = "incidents" }]
}
			`,
		},
		{
			name: "tier object",
			alerts: `
  alerts = {
    fast = terraform_data.fast.output
  }
			`,
			dependency: `
resource "terraform_data" "fast" {
  input = { channel_assignments = [{ channel_id = "incidents" }] }
}
			`,
		},
		{
			name:   "alerts object",
			alerts: "  alerts = terraform_data.alerts.output\n",
			dependency: `
resource "terraform_data" "alerts" {
  input = {
    fast = { channel_assignments = [{ channel_id = "incidents" }] }
  }
}
			`,
		},
		{
			name: "nested schedule ID",
			alerts: `
  alerts = {
    fast = {
      channel_assignments = [{
        channel_id  = "incidents"
        schedule_id = terraform_data.schedule.id
      }]
    }
  }
			`,
			dependency: `
resource "terraform_data" "schedule" {
  input = "office-hours"
}
			`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := fakeSloLogfire(t, "applies-alerts")
			defer server.Close()
			withUnknown := testSloDeliveryReleaseConfig(server.URL, tt.alerts) + tt.dependency
			resource.Test(t, resource.TestCase{
				IsUnitTest:               true,
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: testSloDeliveryReleaseConfig(server.URL, testSloFastChannels)},
					{Config: withUnknown},
					{
						Config: withUnknown,
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
						},
					},
				},
			})
		})
	}
}

func TestSloPlanAlerts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := sloStateFrom(t, sloReadWithTiers(
		[]logclient.ChannelAssignment{testPagerduty},
		[]logclient.ChannelAssignment{testIncidents},
		[]logclient.ChannelAssignment{testReliability},
	))
	stateMedium := sloTier(t, state, "medium")

	// What Terraform plans for `alerts = { fast = { channel_assignments = ... } }`
	// on an update: every computed value, and every other tier, unknown.
	fast, diags := types.ObjectValueFrom(ctx, sloTierAttrTypes, sloTierModel{
		ChannelAssignments: testChannelAssignments(t, testPagerduty, testIncidents),
		AlertID:            types.StringUnknown(), Severity: types.StringUnknown(), Viable: types.BoolUnknown(),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	configFast, diags := types.ObjectValueFrom(ctx, sloTierAttrTypes, sloTierModel{
		ChannelAssignments: testChannelAssignments(t, testPagerduty, testIncidents),
		AlertID:            types.StringNull(), Severity: types.StringNull(), Viable: types.BoolNull(),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	config, diags := types.ObjectValue(sloAlertsAttrTypes, map[string]attr.Value{
		"fast": configFast, "medium": types.ObjectNull(sloTierAttrTypes), "slow": types.ObjectNull(sloTierAttrTypes),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	plan, diags := types.ObjectValue(sloAlertsAttrTypes, map[string]attr.Value{
		"fast": fast, "medium": types.ObjectUnknown(sloTierAttrTypes), "slow": types.ObjectUnknown(sloTierAttrTypes),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}

	for _, tt := range []struct {
		name          string
		targetChanged bool
		wantViable    types.Bool
	}{
		{"same target keeps viable", false, types.BoolValue(true)},
		{"changed target leaves viable unknown", true, types.BoolUnknown()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, diags := sloPlanAlerts(ctx, config, plan, state.Alerts, tt.targetChanged)
			if diags.HasError() {
				t.Fatal(diags)
			}
			m := state
			m.Alerts = got
			f := sloTier(t, m, "fast")
			if !f.ChannelAssignments.Equal(testChannelAssignments(t, testPagerduty, testIncidents)) ||
				f.AlertID.ValueString() != "alert-fast" || f.Severity.ValueString() != "page" || !f.Viable.Equal(tt.wantViable) {
				t.Fatalf("fast: got %+v", f)
			}
			medium := sloTier(t, m, "medium")
			if !medium.ChannelAssignments.Equal(stateMedium.ChannelAssignments) || !medium.AlertID.Equal(stateMedium.AlertID) || !medium.Viable.Equal(tt.wantViable) {
				t.Fatalf("medium: got %+v", medium)
			}
		})
	}
}
