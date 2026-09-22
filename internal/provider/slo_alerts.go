// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// sloDeliveryRelease names the first Logfire release that applies per-tier
// `alerts` on SLO writes. v2026-09-22.02 returns an SLO's tier alerts but
// ignores `alerts` on SLO create and update.
const sloDeliveryRelease = "the first Logfire release after v2026-09-22.02"

// sloTiers are the SLO's burn-rate tiers, in the order the API documents them.
var sloTiers = []string{"fast", "medium", "slow"}

// sloAlertsModel is the `alerts` attribute: one object per burn-rate tier.
type sloAlertsModel struct {
	Fast   types.Object `tfsdk:"fast"`
	Medium types.Object `tfsdk:"medium"`
	Slow   types.Object `tfsdk:"slow"`
}

func (m *sloAlertsModel) tier(name string) *types.Object {
	switch name {
	case "fast":
		return &m.Fast
	case "medium":
		return &m.Medium
	default:
		return &m.Slow
	}
}

// sloTierModel is one tier's alert. `channel_assignments` uses the same
// schema, model and conversion as `logfire_alert.channel_assignments`.
type sloTierModel struct {
	ChannelAssignments types.Set    `tfsdk:"channel_assignments"`
	AlertID            types.String `tfsdk:"alert_id"`
	Severity           types.String `tfsdk:"severity"`
	Viable             types.Bool   `tfsdk:"viable"`
}

var sloTierAttrTypes = map[string]attr.Type{
	"channel_assignments": types.SetType{ElemType: channelAssignmentObjectType},
	"alert_id":            types.StringType,
	"severity":            types.StringType,
	"viable":              types.BoolType,
}

var sloAlertsAttrTypes = map[string]attr.Type{
	"fast":   types.ObjectType{AttrTypes: sloTierAttrTypes},
	"medium": types.ObjectType{AttrTypes: sloTierAttrTypes},
	"slow":   types.ObjectType{AttrTypes: sloTierAttrTypes},
}

func sloAlertsAttribute() rschema.SingleNestedAttribute {
	tierAttribute := func(tier, severity string) rschema.SingleNestedAttribute {
		return rschema.SingleNestedAttribute{
			Optional: true,
			Computed: true,
			MarkdownDescription: "The `" + tier + "` burn-rate tier's alert (severity `" + severity + "`). " +
				"Omit it to leave that alert's channels as they are.",
			Attributes: map[string]rschema.Attribute{
				"channel_assignments": channelAssignmentsAttribute(
					"Channels of this tier's alert, each with an optional delivery schedule. "+
						"The provider writes the value to the alert on create, and on update when it differs from what the alert has. "+
						"It is read back from the alert, so a change made on the Logfire alerts page shows as drift. "+
						"Set it to `[]` to remove every channel. It is the same type as `logfire_alert.channel_assignments`.",
					channelAssignmentsOptionalComputed,
				),
				"alert_id": rschema.StringAttribute{
					Computed: true,
					MarkdownDescription: "ID of the tier's alert. Null only for an SLO created before Logfire kept all three tier alerts, " +
						"when the tier could not fire at the SLO's target. The provider then plans an update of the SLO, " +
						"even when no attribute changed, and that update creates the missing alert.",
				},
				"severity": rschema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "`page` for the `fast` and `medium` tiers, `ticket` for the `slow` tier.",
				},
				"viable": rschema.BoolAttribute{
					Computed: true,
					MarkdownDescription: "False when the tier cannot fire at the SLO's `target_percent`. " +
						"The alert keeps its channels but is not evaluated until a target change makes the tier viable.",
				},
			},
		}
	}
	return rschema.SingleNestedAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: "The SLO's burn-rate alerts, one per tier: `fast` and `medium` (severity `page`) and `slow` (severity `ticket`). " +
			"Each alert exists as long as the SLO does. Configure `channel_assignments` on the tiers whose delivery Terraform should manage. " +
			"A tier that is not configured is not sent and not managed: the provider reports its current channels and never changes them. " +
			"Setting `channel_assignments` needs " + sloDeliveryRelease + ". An older release does not apply them, so the provider fails the apply with an error. " +
			"`alerts` is null when the Logfire release does not return the SLO's alerts.",
		Attributes: map[string]rschema.Attribute{
			"fast":   tierAttribute("fast", "page"),
			"medium": tierAttribute("medium", "page"),
			"slow":   tierAttribute("slow", "ticket"),
		},
	}
}

// sloTierFromObject reads one tier object. ok is false when the object is
// null or unknown.
func sloTierFromObject(ctx context.Context, obj types.Object) (sloTierModel, bool, diag.Diagnostics) {
	var t sloTierModel
	if obj.IsNull() || obj.IsUnknown() {
		return t, false, nil
	}
	diags := obj.As(ctx, &t, basetypes.ObjectAsOptions{})
	return t, !diags.HasError(), diags
}

// sloAlertsFromObject reads the `alerts` attribute. ok is false when it is
// null or unknown.
func sloAlertsFromObject(ctx context.Context, obj types.Object) (sloAlertsModel, bool, diag.Diagnostics) {
	var a sloAlertsModel
	if obj.IsNull() || obj.IsUnknown() {
		return a, false, nil
	}
	diags := obj.As(ctx, &a, basetypes.ObjectAsOptions{})
	return a, !diags.HasError(), diags
}

// sloAlertsDelivery builds the request's per-tier delivery. A tier is included
// when the plan has known channel assignments for it and include accepts
// them. It returns nil when no tier is included, so the request omits
// `alerts` and the API leaves every alert's channels as they are.
func sloAlertsDelivery(ctx context.Context, plan types.Object, include func(tier string, planned types.Set) bool) (*logclient.SloAlertsDelivery, diag.Diagnostics) {
	alerts, ok, diags := sloAlertsFromObject(ctx, plan)
	if !ok {
		return nil, diags
	}
	var out logclient.SloAlertsDelivery
	included := false
	for _, name := range sloTiers {
		t, ok, d := sloTierFromObject(ctx, *alerts.tier(name))
		diags.Append(d...)
		if !ok || t.ChannelAssignments.IsNull() || t.ChannelAssignments.IsUnknown() || !include(name, t.ChannelAssignments) {
			continue
		}
		assignments, d := channelAssignmentsToAPI(ctx, t.ChannelAssignments)
		diags.Append(d...)
		if diags.HasError() {
			return nil, diags
		}
		*out.Tier(name) = &logclient.SloTierDelivery{ChannelAssignments: assignments}
		included = true
	}
	if !included {
		return nil, diags
	}
	return &out, diags
}

// sloStateTierAssignments returns a tier's channel assignments in state, or
// a null set when state has none.
func sloStateTierAssignments(ctx context.Context, state types.Object, tier string) (types.Set, diag.Diagnostics) {
	alerts, ok, diags := sloAlertsFromObject(ctx, state)
	if !ok {
		return types.SetNull(channelAssignmentObjectType), diags
	}
	t, ok, d := sloTierFromObject(ctx, *alerts.tier(tier))
	diags.Append(d...)
	if !ok {
		return types.SetNull(channelAssignmentObjectType), diags
	}
	return t.ChannelAssignments, diags
}

// sloTiersWithoutAlert returns the tiers whose alert is missing in state.
func sloTiersWithoutAlert(ctx context.Context, state types.Object) ([]string, diag.Diagnostics) {
	alerts, ok, diags := sloAlertsFromObject(ctx, state)
	if !ok {
		return nil, diags
	}
	var missing []string
	for _, name := range sloTiers {
		t, ok, d := sloTierFromObject(ctx, *alerts.tier(name))
		diags.Append(d...)
		if ok && t.AlertID.IsNull() {
			missing = append(missing, name)
		}
	}
	return missing, diags
}

// sloAlertsToObject converts the API's per-tier alerts. A Logfire release that
// does not return them (nil) keeps the known values of prior and turns
// unknown ones into null, so a create or update never leaves an unknown value
// in state.
func sloAlertsToObject(ctx context.Context, api *logclient.SloTierAlerts, prior types.Object) (types.Object, diag.Diagnostics) {
	if api == nil {
		return sloAlertsWithoutAPI(ctx, prior)
	}
	tiers := map[string]attr.Value{}
	var diags diag.Diagnostics
	for _, name := range sloTiers {
		a := api.Tier(name)
		set, d := channelAssignmentsFromAPI(ctx, a.ChannelAssignments)
		diags.Append(d...)
		t := sloTierModel{
			ChannelAssignments: set,
			AlertID:            types.StringNull(),
			Severity:           types.StringValue(a.Severity),
			Viable:             types.BoolValue(a.Viable),
		}
		if a.AlertID != nil {
			t.AlertID = types.StringValue(*a.AlertID)
		}
		obj, d := types.ObjectValueFrom(ctx, sloTierAttrTypes, t)
		diags.Append(d...)
		tiers[name] = obj
	}
	if diags.HasError() {
		return types.ObjectNull(sloAlertsAttrTypes), diags
	}
	return types.ObjectValue(sloAlertsAttrTypes, tiers)
}

func sloAlertsWithoutAPI(ctx context.Context, prior types.Object) (types.Object, diag.Diagnostics) {
	alerts, ok, diags := sloAlertsFromObject(ctx, prior)
	if !ok {
		return types.ObjectNull(sloAlertsAttrTypes), diags
	}
	tiers := map[string]attr.Value{}
	for _, name := range sloTiers {
		t, ok, d := sloTierFromObject(ctx, *alerts.tier(name))
		diags.Append(d...)
		if !ok {
			tiers[name] = types.ObjectNull(sloTierAttrTypes)
			continue
		}
		if t.ChannelAssignments.IsUnknown() {
			t.ChannelAssignments = types.SetNull(channelAssignmentObjectType)
		}
		if t.AlertID.IsUnknown() {
			t.AlertID = types.StringNull()
		}
		if t.Severity.IsUnknown() {
			t.Severity = types.StringNull()
		}
		if t.Viable.IsUnknown() {
			t.Viable = types.BoolNull()
		}
		obj, d := types.ObjectValueFrom(ctx, sloTierAttrTypes, t)
		diags.Append(d...)
		tiers[name] = obj
	}
	if diags.HasError() {
		return types.ObjectNull(sloAlertsAttrTypes), diags
	}
	return types.ObjectValue(sloAlertsAttrTypes, tiers)
}

// sloDeliveryApplied returns an error when an SLO create or update response
// does not show the channel assignments that the request sent. An older
// Logfire release ignores `alerts` on SLO writes and still answers with
// success, so without this check the apply would succeed and leave the SLO's
// alerts without the configured channels.
func sloDeliveryApplied(sent *logclient.SloAlertsDelivery, got *logclient.SloTierAlerts) diag.Diagnostics {
	if sent == nil {
		return nil
	}
	var cause string
	if got == nil {
		cause = "This Logfire release does not return per-tier `alerts` on SLOs."
	} else {
		var tiers []string
		for _, name := range sloTiers {
			want := *sent.Tier(name)
			if want != nil && !sameChannelAssignments(want.ChannelAssignments, got.Tier(name).ChannelAssignments) {
				tiers = append(tiers, "`"+name+"`")
			}
		}
		if len(tiers) == 0 {
			return nil
		}
		cause = "Logfire did not apply the channel assignments sent for the " + strings.Join(tiers, ", ") +
			" tier alert, because this Logfire release ignores `alerts` on SLO create and update."
	}
	return diag.Diagnostics{diag.NewAttributeErrorDiagnostic(
		path.Root("alerts"),
		"Logfire release too old for SLO channel assignments",
		cause+" Setting SLO channels per tier alert needs "+sloDeliveryRelease+". "+
			"The SLO was saved, but its alerts do not have the configured channels. "+
			"Upgrade Logfire and apply again, or remove `alerts` from the configuration until then.",
	)}
}

// sameChannelAssignments compares two lists of assignments as sets. The API
// keeps one assignment per channel and returns them in its own order.
func sameChannelAssignments(a, b []logclient.ChannelAssignment) bool {
	if len(a) != len(b) {
		return false
	}
	schedules := make(map[string]string, len(a))
	for _, x := range a {
		schedules[x.ChannelID] = scheduleIDOrEmpty(x.ScheduleID)
	}
	for _, y := range b {
		s, ok := schedules[y.ChannelID]
		if !ok || s != scheduleIDOrEmpty(y.ScheduleID) {
			return false
		}
	}
	return true
}

func scheduleIDOrEmpty(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}
