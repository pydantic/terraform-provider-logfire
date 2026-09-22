// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func testChannelAssignments(t *testing.T, in ...logclient.ChannelAssignment) types.Set {
	t.Helper()
	set, diags := channelAssignmentsFromAPI(context.Background(), in)
	if diags.HasError() {
		t.Fatalf("failed to build channel assignments: %v", diags)
	}
	return set
}

func TestChannelAssignmentsRoundTrip(t *testing.T) {
	t.Parallel()

	set := testChannelAssignments(t,
		logclient.ChannelAssignment{ChannelID: "pagerduty"},
		logclient.ChannelAssignment{ChannelID: "incidents", ScheduleID: stringPtr("office-hours")},
	)
	out, diags := channelAssignmentsToAPI(context.Background(), set)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := map[string]*string{}
	for _, a := range out {
		got[a.ChannelID] = a.ScheduleID
	}
	if len(got) != 2 || got["pagerduty"] != nil || got["incidents"] == nil || *got["incidents"] != "office-hours" {
		t.Fatalf("unexpected assignments: %#v", out)
	}

	// An empty set is sent as `[]`, which removes every channel.
	empty, diags := channelAssignmentsToAPI(context.Background(), testChannelAssignments(t))
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	b, err := json.Marshal(logclient.SloUpdate{Alerts: &logclient.SloAlertsDelivery{Slow: &logclient.SloTierDelivery{ChannelAssignments: empty}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"alerts":{"slow":{"channel_assignments":[]}}}` {
		t.Fatalf("unexpected body: %s", b)
	}
}

func TestChannelAssignmentJSON(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal([]logclient.ChannelAssignment{
		{ChannelID: "pagerduty"},
		{ChannelID: "incidents", ScheduleID: stringPtr("office-hours")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"channel_id":"pagerduty"},{"channel_id":"incidents","schedule_id":"office-hours"}]`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}

	// An SLO update without channel changes names no channel field, so the
	// API leaves the alerts' channels as they are.
	name := "renamed"
	b, err = json.Marshal(logclient.SloUpdate{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "channel") {
		t.Fatalf("expected no channel field, got %s", b)
	}
}

func TestUniqueChannelIDsValidator(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		in      []logclient.ChannelAssignment
		wantErr bool
	}{
		{"distinct channels", []logclient.ChannelAssignment{{ChannelID: "a"}, {ChannelID: "b", ScheduleID: stringPtr("s")}}, false},
		{"same channel with two schedules", []logclient.ChannelAssignment{{ChannelID: "a"}, {ChannelID: "a", ScheduleID: stringPtr("s")}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var resp validator.SetResponse
			uniqueChannelIDsValidator{}.ValidateSet(context.Background(), validator.SetRequest{
				Path:        path.Root("channel_assignments"),
				ConfigValue: testChannelAssignments(t, tt.in...),
			}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("error = %v, want %v: %v", resp.Diagnostics.HasError(), tt.wantErr, resp.Diagnostics)
			}
		})
	}
}

func TestAlertChannelAssignmentsMapping(t *testing.T) {
	t.Parallel()

	m := environmentsBaseModel(t)
	m.ChannelAssignments = testChannelAssignments(t,
		logclient.ChannelAssignment{ChannelID: "pagerduty"},
		logclient.ChannelAssignment{ChannelID: "incidents", ScheduleID: stringPtr("office-hours")},
	)
	create, diags := alertModelToCreate(context.Background(), &m)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	b, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "channel_ids") || !strings.Contains(string(b), `"channel_assignments":[`) {
		t.Fatalf("expected only channel_assignments in the body, got %s", b)
	}

	// The alert API returns the assignments in the shape it accepts.
	var read logclient.AlertRead
	if err := json.Unmarshal([]byte(`{
		"id": "alert-1", "name": "name", "query": "select 1", "time_window": "PT5M", "frequency": "PT5M",
		"watermark": "PT10S", "notify_when": "has_matches", "active": true,
		"channel_assignments": [
			{"channel_id": "incidents", "schedule_id": "office-hours"},
			{"channel_id": "pagerduty", "schedule_id": null}
		]
	}`), &read); err != nil {
		t.Fatal(err)
	}
	var got AlertModel
	if diags := alertReadToModel(context.Background(), &read, &got); diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !got.ChannelAssignments.Equal(m.ChannelAssignments) {
		t.Fatalf("got %v, want %v", got.ChannelAssignments, m.ChannelAssignments)
	}
}

// TestAlertReadWithoutChannelAssignmentsUsesChannels checks the read of a
// Logfire release that returns an alert's assignments only inside `channels`.
func TestAlertReadWithoutChannelAssignmentsUsesChannels(t *testing.T) {
	t.Parallel()

	var read logclient.AlertRead
	if err := json.Unmarshal([]byte(`{
		"id": "alert-1", "name": "name", "query": "select 1", "time_window": "PT5M", "frequency": "PT5M",
		"watermark": "PT10S", "notify_when": "has_matches", "active": true,
		"channels": [{"id": "pagerduty", "schedule_id": null}, {"id": "incidents", "schedule_id": "office-hours"}]
	}`), &read); err != nil {
		t.Fatal(err)
	}
	var got AlertModel
	if diags := alertReadToModel(context.Background(), &read, &got); diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if want := testChannelAssignments(t, testPagerduty, testIncidents); !got.ChannelAssignments.Equal(want) {
		t.Fatalf("got %v, want %v", got.ChannelAssignments, want)
	}
}
