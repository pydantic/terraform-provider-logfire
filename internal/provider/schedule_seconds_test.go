// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// fakeScheduleLogfire serves the schedule routes as Logfire does: it stores
// the windows it receives and returns every time as "HH:MM:SS". It starts
// with one schedule, `seeded`, whose times have non-zero seconds.
func fakeScheduleLogfire(t *testing.T) *httptest.Server {
	t.Helper()
	const seeded = "55555555-5555-5555-5555-555555555555"
	type window struct {
		Days      []int  `json:"days"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	type schedule struct {
		Label    string   `json:"label"`
		Timezone string   `json:"timezone"`
		Windows  []window `json:"windows"`
	}
	var mu sync.Mutex
	schedules := map[string]schedule{
		seeded: {Label: "Night shift", Timezone: "UTC", Windows: []window{{Days: []int{1}, StartTime: "08:15:30", EndTime: "17:45:59"}}},
	}
	withSeconds := func(s string) string {
		for _, layout := range []string{"15:04:05", "15:04"} {
			if v, err := time.Parse(layout, s); err == nil {
				return v.Format("15:04:05")
			}
		}
		t.Errorf("invalid time %q", s)
		return s
	}
	respond := func(w http.ResponseWriter, status int, id string) {
		s := schedules[id]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "organization_id": "22222222-2222-2222-2222-222222222222", "label": s.Label,
			"timezone": s.Timezone, "windows": s.Windows, "created_at": "2026-09-22T00:00:00Z", "updated_at": nil,
		})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/schedules/"), "/")
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			var in schedule
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("request body: %v", err)
			}
			for i := range in.Windows {
				in.Windows[i].StartTime = withSeconds(in.Windows[i].StartTime)
				in.Windows[i].EndTime = withSeconds(in.Windows[i].EndTime)
			}
			if r.Method == http.MethodPost {
				id = "33333333-3333-3333-3333-333333333333"
			}
			schedules[id] = in
		}
		_, exists := schedules[id]
		switch {
		case r.Method == http.MethodPost && id != "":
			respond(w, http.StatusCreated, id)
		case (r.Method == http.MethodGet || r.Method == http.MethodPut) && exists:
			respond(w, http.StatusOK, id)
		case r.Method == http.MethodDelete && exists:
			delete(schedules, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func testScheduleSecondsConfig(baseURL, label, start, end string) string {
	return fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = "test-token"
}

resource "logfire_schedule" "test" {
  label    = %q
  timezone = "UTC"
  windows = [
    { days = [1], start_time = %q, end_time = %q },
  ]
}
`, baseURL, label, start, end)
}

// TestScheduleTimesRoundTrip applies `HH:MM` and `HH:MM:SS` times, which the
// API returns as `HH:MM:SS`, and checks that neither shows a diff afterwards
// and that an import reads the same values.
func TestScheduleTimesRoundTrip(t *testing.T) {
	server := fakeScheduleLogfire(t)
	defer server.Close()
	config := testScheduleSecondsConfig(server.URL, "Office hours", "09:00", "17:30:15")
	resource.Test(t, resource.TestCase{
		IsUnitTest:               true,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_schedule.test", tfjsonpath.New("windows").AtSliceIndex(0).AtMapKey("start_time"), knownvalue.StringExact("09:00")),
					statecheck.ExpectKnownValue("logfire_schedule.test", tfjsonpath.New("windows").AtSliceIndex(0).AtMapKey("end_time"), knownvalue.StringExact("17:30:15")),
				},
			},
			{
				Config:            config,
				ResourceName:      "logfire_schedule.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestScheduleImportWithSeconds imports a schedule whose times have non-zero
// seconds. The imported values must be valid configuration: the same values
// in configuration then plan no change.
func TestScheduleImportWithSeconds(t *testing.T) {
	server := fakeScheduleLogfire(t)
	defer server.Close()
	config := testScheduleSecondsConfig(server.URL, "Night shift", "08:15:30", "17:45:59")
	resource.Test(t, resource.TestCase{
		IsUnitTest:               true,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       "logfire_schedule.test",
				ImportState:        true,
				ImportStateId:      "55555555-5555-5555-5555-555555555555",
				ImportStatePersist: true,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestScheduleTimeValueValidates checks that every value a read can write to
// state passes the schema validator.
func TestScheduleTimeValueValidates(t *testing.T) {
	t.Parallel()
	var schemaResp fwresource.SchemaResponse
	(&ScheduleResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, &schemaResp)
	windows, ok := schemaResp.Schema.Attributes["windows"].(rschema.ListNestedAttribute)
	if !ok {
		t.Fatal("windows is not a list of objects")
	}
	for _, name := range []string{"start_time", "end_time"} {
		attribute, ok := windows.NestedObject.Attributes[name].(rschema.StringAttribute)
		if !ok || len(attribute.Validators) == 0 {
			t.Fatalf("%s has no validators", name)
		}
		checkScheduleTimeStateValues(t, name, attribute.Validators)
	}
}

func checkScheduleTimeStateValues(t *testing.T, name string, validators []validator.String) {
	t.Helper()
	for _, api := range []string{"00:00:00", "09:00:00", "08:15:30", "23:59:59", "09:30"} {
		value := scheduleTimeValue(api, types.StringNull())
		for _, v := range validators {
			resp := &validator.StringResponse{}
			v.ValidateString(t.Context(), validator.StringRequest{Path: path.Root(name), ConfigValue: value}, resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("%s: state value %q from API %q does not validate: %v", name, value.ValueString(), api, resp.Diagnostics)
			}
		}
	}
}
