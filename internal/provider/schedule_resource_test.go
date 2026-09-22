// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// scheduleTransport answers the schedule routes from fixed bodies and records
// each request body.
type scheduleTransport struct {
	response string
	notFound bool
	bodies   *[]string
}

func (t scheduleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status, body := http.StatusOK, t.response
	switch {
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/api/v1/schedules/") && req.URL.Path != "/api/v1/schedules/":
		if t.notFound {
			status, body = http.StatusNotFound, `{"detail":"No schedule with that ID in this organization."}`
		}
	case req.Method == http.MethodPost && req.URL.Path == "/api/v1/schedules/":
		status = http.StatusCreated
	case req.Method == http.MethodPut && strings.HasPrefix(req.URL.Path, "/api/v1/schedules/"):
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
	if req.Body != nil && t.bodies != nil {
		b, _ := io.ReadAll(req.Body)
		*t.bodies = append(*t.bodies, string(b))
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

const officeHoursRead = `{"id":"33333333-3333-3333-3333-333333333333","organization_id":"22222222-2222-2222-2222-222222222222",` +
	`"label":"Office hours","timezone":"Europe/London",` +
	`"windows":[{"days":[1,2,3,4,5],"start_time":"09:00:00","end_time":"18:00:00"}],` +
	`"created_at":"2026-09-21T00:00:00Z","updated_at":null}`

func scheduleSchemaState(t *testing.T) (resource.SchemaResponse, tftypes.Value) {
	t.Helper()
	r := &ScheduleResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	if schemaResponse.Diagnostics.HasError() {
		t.Fatal(schemaResponse.Diagnostics)
	}
	return schemaResponse, tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), nil)
}

func officeHoursModel(t *testing.T, start, end string) ScheduleModel {
	t.Helper()
	ctx := context.Background()
	days, diags := types.ListValueFrom(ctx, types.Int64Type, []int64{1, 2, 3, 4, 5})
	if diags.HasError() {
		t.Fatal(diags)
	}
	windows, diags := types.ListValueFrom(ctx, scheduleWindowObjectType, []scheduleWindowModel{
		{Days: days, StartTime: types.StringValue(start), EndTime: types.StringValue(end)},
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	return ScheduleModel{
		ID:       types.StringUnknown(),
		Label:    types.StringValue("Office hours"),
		Timezone: types.StringValue("Europe/London"),
		Windows:  windows,
	}
}

func TestScheduleCreateSendsAndReadsWindows(t *testing.T) {
	t.Parallel()
	var bodies []string
	c, err := logclient.NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: scheduleTransport{response: officeHoursRead, bodies: &bodies}})
	if err != nil {
		t.Fatal(err)
	}
	r := &ScheduleResource{client: c}
	schemaResponse, raw := scheduleSchemaState(t)

	plan := tfsdk.Plan{Schema: schemaResponse.Schema, Raw: raw}
	model := officeHoursModel(t, "09:00", "18:00")
	if diags := plan.Set(t.Context(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema, Raw: raw}}
	r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}

	want := `{"label":"Office hours","timezone":"Europe/London","windows":[{"days":[1,2,3,4,5],"start_time":"09:00","end_time":"18:00"}]}`
	if len(bodies) != 1 || bodies[0] != want {
		t.Fatalf("unexpected request bodies: %v", bodies)
	}

	var state ScheduleModel
	if diags := resp.State.Get(t.Context(), &state); diags.HasError() {
		t.Fatal(diags)
	}
	if state.ID.ValueString() != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("unexpected id: %v", state.ID)
	}
	// The API returns "HH:MM:SS"; the configured "HH:MM" stays in state.
	if !state.Windows.Equal(model.Windows) {
		t.Fatalf("windows: got %v, want %v", state.Windows, model.Windows)
	}
}

func TestScheduleTimeValue(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		api   string
		prior types.String
		want  string
	}{
		{"09:00:00", types.StringValue("09:00"), "09:00"},
		{"09:00:00", types.StringNull(), "09:00"},
		{"09:30:00", types.StringValue("09:00"), "09:30"},
		{"09:30:15", types.StringNull(), "09:30:15"},
		{"09:30", types.StringNull(), "09:30"},
	} {
		if got := scheduleTimeValue(tt.api, tt.prior).ValueString(); got != tt.want {
			t.Errorf("scheduleTimeValue(%q, %v) = %q, want %q", tt.api, tt.prior, got, tt.want)
		}
	}
}

func TestScheduleUpdateSendsEveryField(t *testing.T) {
	t.Parallel()
	var bodies []string
	c, err := logclient.NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: scheduleTransport{response: officeHoursRead, bodies: &bodies}})
	if err != nil {
		t.Fatal(err)
	}
	r := &ScheduleResource{client: c}
	schemaResponse, raw := scheduleSchemaState(t)

	prior := officeHoursModel(t, "08:00", "18:00")
	prior.ID = types.StringValue("33333333-3333-3333-3333-333333333333")
	next := officeHoursModel(t, "09:00", "18:00")
	next.ID = prior.ID

	state := tfsdk.State{Schema: schemaResponse.Schema, Raw: raw}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema, Raw: raw}
	if diags := state.Set(t.Context(), &prior); diags.HasError() {
		t.Fatal(diags)
	}
	if diags := plan.Set(t.Context(), &next); diags.HasError() {
		t.Fatal(diags)
	}
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(bodies) != 1 {
		t.Fatalf("expected one request, got %v", bodies)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"label", "timezone", "windows"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("expected %q in the update body, got %s", key, bodies[0])
		}
	}
}

// TestScheduleImport imports by UUID. The API has no list route, so there is
// no import by label.
func TestScheduleImport(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		id       string
		notFound bool
		wantErr  bool
	}{
		{"uuid", " 33333333-3333-3333-3333-333333333333 ", false, false},
		{"unknown uuid", "44444444-4444-4444-4444-444444444444", true, true},
		{"empty", " ", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := logclient.NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: scheduleTransport{response: officeHoursRead, notFound: tt.notFound}})
			if err != nil {
				t.Fatal(err)
			}
			r := &ScheduleResource{client: c}
			schemaResponse, raw := scheduleSchemaState(t)
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResponse.Schema, Raw: raw}}
			r.ImportState(t.Context(), resource.ImportStateRequest{ID: tt.id}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("error = %v, want %v: %v", resp.Diagnostics.HasError(), tt.wantErr, resp.Diagnostics)
			}
			if tt.wantErr {
				return
			}
			var id types.String
			if diags := resp.State.GetAttribute(t.Context(), path.Root("id"), &id); diags.HasError() {
				t.Fatal(diags)
			}
			if id.ValueString() != "33333333-3333-3333-3333-333333333333" {
				t.Fatalf("unexpected id: %v", id)
			}
		})
	}
}
