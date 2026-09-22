// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
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
