// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var (
	testPagerduty   = logclient.ChannelAssignment{ChannelID: "pagerduty"}
	testIncidents   = logclient.ChannelAssignment{ChannelID: "incidents", ScheduleID: stringPtr("office-hours")}
	testReliability = logclient.ChannelAssignment{ChannelID: "reliability"}
)

func sloReadWithTiers(fast, medium, slow []logclient.ChannelAssignment) *logclient.SloRead {
	read := sloRead()
	read.Alerts = &logclient.SloTierAlerts{
		Fast:   logclient.SloTierAlert{AlertID: stringPtr("alert-fast"), Severity: "page", Viable: true, ChannelAssignments: fast},
		Medium: logclient.SloTierAlert{AlertID: stringPtr("alert-medium"), Severity: "page", Viable: true, ChannelAssignments: medium},
		Slow:   logclient.SloTierAlert{AlertID: stringPtr("alert-slow"), Severity: "ticket", Viable: true, ChannelAssignments: slow},
	}
	return read
}

// sloStateFrom maps an API read onto a fresh model, as a refresh does.
func sloStateFrom(t *testing.T, read *logclient.SloRead) SloModel {
	t.Helper()
	m := baseSloModel()
	m.ID = types.StringValue("slo-1")
	m.Environments = types.SetNull(types.StringType)
	m.Alerts = types.ObjectNull(sloAlertsAttrTypes)
	if diags := sloReadToModel(context.Background(), read, &m); diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return m
}

func sloTier(t *testing.T, m SloModel, name string) sloTierModel {
	t.Helper()
	alerts, ok, diags := sloAlertsFromObject(context.Background(), m.Alerts)
	if diags.HasError() || !ok {
		t.Fatalf("alerts not known: %v", diags)
	}
	tier, ok, diags := sloTierFromObject(context.Background(), *alerts.tier(name))
	if diags.HasError() || !ok {
		t.Fatalf("tier %s not known: %v", name, diags)
	}
	return tier
}

// withTierAssignments returns m with the channel assignments of one tier
// replaced, as a configuration of that tier would plan them.
func withTierAssignments(t *testing.T, m SloModel, name string, set types.Set) SloModel {
	t.Helper()
	ctx := context.Background()
	alerts, ok, diags := sloAlertsFromObject(ctx, m.Alerts)
	if diags.HasError() || !ok {
		t.Fatalf("alerts not known: %v", diags)
	}
	tier, _, diags := sloTierFromObject(ctx, *alerts.tier(name))
	if diags.HasError() {
		t.Fatal(diags)
	}
	tier.ChannelAssignments = set
	obj, diags := types.ObjectValueFrom(ctx, sloTierAttrTypes, tier)
	if diags.HasError() {
		t.Fatal(diags)
	}
	*alerts.tier(name) = obj
	m.Alerts, diags = types.ObjectValueFrom(ctx, sloAlertsAttrTypes, alerts)
	if diags.HasError() {
		t.Fatal(diags)
	}
	return m
}

// sloTierObject is a tier object holding assignments. Its computed values are
// unknown, as in a plan, or null, as in a configuration.
func sloTierObject(t *testing.T, assignments types.Set, unknown bool) types.Object {
	t.Helper()
	tier := sloTierModel{
		ChannelAssignments: assignments,
		AlertID:            types.StringNull(),
		Severity:           types.StringNull(),
		Viable:             types.BoolNull(),
	}
	if unknown {
		tier.AlertID = types.StringUnknown()
		tier.Severity = types.StringUnknown()
		tier.Viable = types.BoolUnknown()
	}
	obj, diags := types.ObjectValueFrom(context.Background(), sloTierAttrTypes, tier)
	if diags.HasError() {
		t.Fatal(diags)
	}
	return obj
}

func sloAlertsObject(t *testing.T, fast, medium, slow types.Object) types.Object {
	t.Helper()
	tiers := map[string]attr.Value{"fast": fast, "medium": medium, "slow": slow}
	obj, diags := types.ObjectValue(sloAlertsAttrTypes, tiers)
	if diags.HasError() {
		t.Fatal(diags)
	}
	return obj
}

func TestSloReadToModelAlerts(t *testing.T) {
	t.Parallel()

	read := sloReadWithTiers(
		[]logclient.ChannelAssignment{testPagerduty, testIncidents},
		[]logclient.ChannelAssignment{testIncidents},
		[]logclient.ChannelAssignment{testReliability},
	)
	read.Alerts.Fast.Viable = false
	read.Alerts.Medium.AlertID = nil
	m := sloStateFrom(t, read)

	for _, tt := range []struct {
		tier     string
		want     types.Set
		alertID  types.String
		severity string
		viable   bool
	}{
		{"fast", testChannelAssignments(t, testPagerduty, testIncidents), types.StringValue("alert-fast"), "page", false},
		{"medium", testChannelAssignments(t, testIncidents), types.StringNull(), "page", true},
		{"slow", testChannelAssignments(t, testReliability), types.StringValue("alert-slow"), "ticket", true},
	} {
		got := sloTier(t, m, tt.tier)
		if !got.ChannelAssignments.Equal(tt.want) || !got.AlertID.Equal(tt.alertID) ||
			got.Severity.ValueString() != tt.severity || got.Viable.ValueBool() != tt.viable {
			t.Fatalf("%s: got %+v", tt.tier, got)
		}
	}
}

func TestSloModelToCreateAlerts(t *testing.T) {
	t.Parallel()

	// A configured tier is sent with its assignments, including `[]`; a tier
	// whose assignments are unknown (not configured) is left out.
	m := baseSloModel()
	m.Alerts = sloAlertsObject(
		t,
		sloTierObject(t, testChannelAssignments(t, testPagerduty), true),
		types.ObjectUnknown(sloTierAttrTypes),
		sloTierObject(t, testChannelAssignments(t), true),
	)

	b, err := json.Marshal(sloCreate(t, m).Alerts)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"fast":{"channel_assignments":[{"channel_id":"pagerduty"}]},"slow":{"channel_assignments":[]}}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func TestSloModelToUpdateAlerts(t *testing.T) {
	t.Parallel()

	state := sloStateFrom(t, sloReadWithTiers(
		[]logclient.ChannelAssignment{testPagerduty},
		[]logclient.ChannelAssignment{testPagerduty},
		[]logclient.ChannelAssignment{testReliability},
	))

	for _, tt := range []struct {
		name string
		plan SloModel
		want string
	}{
		{"unchanged sends nothing", state, `null`},
		{
			"only the changed tier is sent",
			withTierAssignments(t, state, "medium", testChannelAssignments(t, testPagerduty, testIncidents)),
			`{"medium":{"channel_assignments":[{"channel_id":"incidents","schedule_id":"office-hours"},{"channel_id":"pagerduty"}]}}`,
		},
		{
			"an empty set removes every channel",
			withTierAssignments(t, state, "slow", testChannelAssignments(t)),
			`{"slow":{"channel_assignments":[]}}`,
		},
		{
			"an unknown tier is not sent",
			withTierAssignments(t, state, "fast", types.SetUnknown(channelAssignmentObjectType)),
			`null`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := sloUpdate(t, tt.plan, state)
			if payload.Alerts != nil {
				for _, d := range []*logclient.SloTierDelivery{
					payload.Alerts.Fast, payload.Alerts.Medium, payload.Alerts.Slow,
				} {
					if d != nil {
						sort.Slice(d.ChannelAssignments, func(i, j int) bool {
							return d.ChannelAssignments[i].ChannelID < d.ChannelAssignments[j].ChannelID
						})
					}
				}
			}
			b, err := json.Marshal(payload.Alerts)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tt.want {
				t.Fatalf("got %s, want %s", b, tt.want)
			}
		})
	}
}

func TestSloReadWithoutAPIAlertsKeepsKnownPlanValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A create against a Logfire release that does not return `alerts`: the
	// configured assignments stay, and unknown computed values become null.
	m := baseSloModel()
	m.Alerts = sloAlertsObject(
		t,
		sloTierObject(t, testChannelAssignments(t, testPagerduty), true),
		types.ObjectUnknown(sloTierAttrTypes),
		types.ObjectUnknown(sloTierAttrTypes),
	)
	sloReadInto(t, sloRead(), &m)
	got := sloTier(t, m, "fast")
	if !got.ChannelAssignments.Equal(testChannelAssignments(t, testPagerduty)) ||
		!got.AlertID.IsNull() || !got.Viable.IsNull() {
		t.Fatalf("fast: got %+v", got)
	}
	a, _, _ := sloAlertsFromObject(ctx, m.Alerts)
	if !a.Medium.IsNull() || !a.Slow.IsNull() {
		t.Fatalf("expected null unconfigured tiers, got %v", m.Alerts)
	}
}

// TestSloModifyPlanRepairsMissingTierAlert checks that a tier with no alert
// (an SLO from before every tier kept its alert) plans an update even when no
// attribute changed.
func TestSloModifyPlanRepairsMissingTierAlert(t *testing.T) {
	t.Parallel()

	inSync := sloReadWithTiers([]logclient.ChannelAssignment{testPagerduty}, []logclient.ChannelAssignment{testPagerduty}, nil)
	missing := sloReadWithTiers([]logclient.ChannelAssignment{testPagerduty}, nil, nil)
	missing.Alerts.Medium.AlertID = nil

	for _, tt := range []struct {
		name        string
		read        *logclient.SloRead
		wantUnknown bool
	}{
		{"every tier has an alert", inSync, false},
		{"medium tier has no alert", missing, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			r := &SloResource{}
			var schemaResp resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
			sch := schemaResp.Schema

			model := sloStateFrom(t, tt.read)
			raw := tftypes.NewValue(sch.Type().TerraformType(ctx), nil)
			state := tfsdk.State{Schema: sch, Raw: raw}
			plan := tfsdk.Plan{Schema: sch, Raw: raw}
			if diags := state.Set(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
			if diags := plan.Set(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}

			resp := resource.ModifyPlanResponse{Plan: plan}
			config := tfsdk.Config{Schema: sch, Raw: plan.Raw}
			r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan, Config: config}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var alertID types.String
			if diags := resp.Plan.GetAttribute(ctx, path.Root("alerts").AtName("medium").AtName("alert_id"), &alertID); diags.HasError() {
				t.Fatal(diags)
			}
			if alertID.IsUnknown() != tt.wantUnknown {
				t.Fatalf("medium alert_id unknown = %v, want %v", alertID.IsUnknown(), tt.wantUnknown)
			}
		})
	}
}
