// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// channelAssignmentModel is one element of a `channel_assignments` set. The
// same model backs `logfire_alert.channel_assignments` and
// `logfire_slo.alerts.<tier>.channel_assignments`, so one configuration value
// can be reused for all of them.
type channelAssignmentModel struct {
	ChannelID  types.String `tfsdk:"channel_id"`
	ScheduleID types.String `tfsdk:"schedule_id"`
}

var channelAssignmentAttrTypes = map[string]attr.Type{
	"channel_id":  types.StringType,
	"schedule_id": types.StringType,
}

var channelAssignmentObjectType = types.ObjectType{AttrTypes: channelAssignmentAttrTypes}

// channelAssignmentsMode selects how a `channel_assignments` attribute is set.
type channelAssignmentsMode int

const (
	// channelAssignmentsRequired is configured on every resource instance.
	channelAssignmentsRequired channelAssignmentsMode = iota
	// channelAssignmentsOptionalComputed is read back from the API when the
	// configuration omits it.
	channelAssignmentsOptionalComputed
)

// channelAssignmentsAttribute returns the schema of a set of channel
// assignments. It is a set because the API keeps one assignment per channel
// and returns them in its own order.
func channelAssignmentsAttribute(description string, mode channelAssignmentsMode) rschema.SetNestedAttribute {
	return rschema.SetNestedAttribute{
		Required:            mode == channelAssignmentsRequired,
		Optional:            mode == channelAssignmentsOptionalComputed,
		Computed:            mode == channelAssignmentsOptionalComputed,
		MarkdownDescription: description,
		Validators:          []validator.Set{uniqueChannelIDsValidator{}},
		NestedObject: rschema.NestedAttributeObject{
			Attributes: map[string]rschema.Attribute{
				"channel_id": rschema.StringAttribute{
					Required:            true,
					MarkdownDescription: "ID of the `logfire_channel` to notify.",
					Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				},
				"schedule_id": rschema.StringAttribute{
					Optional: true,
					MarkdownDescription: "ID of a `logfire_schedule`. The channel is notified only inside the schedule's windows. " +
						"Omit it to notify the channel at all times.",
					Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
				},
			},
		},
	}
}

// uniqueChannelIDsValidator rejects two assignments of the same channel. The
// API keeps one assignment per channel, so a second one would be dropped and
// the next plan would show a diff that no apply can resolve.
type uniqueChannelIDsValidator struct{}

func (v uniqueChannelIDsValidator) Description(ctx context.Context) string {
	return "each channel_id appears at most once"
}

func (v uniqueChannelIDsValidator) MarkdownDescription(ctx context.Context) string {
	return "each `channel_id` appears at most once"
}

func (v uniqueChannelIDsValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var elems []channelAssignmentModel
	if diags := req.ConfigValue.ElementsAs(ctx, &elems, false); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	seen := make(map[string]bool, len(elems))
	for _, e := range elems {
		if e.ChannelID.IsNull() || e.ChannelID.IsUnknown() {
			continue
		}
		id := e.ChannelID.ValueString()
		if seen[id] {
			resp.Diagnostics.AddAttributeError(
				req.Path,
				"Duplicate channel assignment",
				fmt.Sprintf("Channel %q is assigned more than once. Assign each channel once, with at most one schedule_id.", id),
			)
			return
		}
		seen[id] = true
	}
}

// channelAssignmentsToAPI converts a known, non-null set to the API type. It
// returns a non-nil slice, so an empty set is sent as `[]` (remove every
// channel) and not omitted.
func channelAssignmentsToAPI(ctx context.Context, set types.Set) ([]logclient.ChannelAssignment, diag.Diagnostics) {
	var elems []channelAssignmentModel
	if diags := set.ElementsAs(ctx, &elems, false); diags.HasError() {
		return nil, diags
	}
	out := make([]logclient.ChannelAssignment, 0, len(elems))
	for _, e := range elems {
		a := logclient.ChannelAssignment{ChannelID: e.ChannelID.ValueString()}
		if !e.ScheduleID.IsNull() && !e.ScheduleID.IsUnknown() && e.ScheduleID.ValueString() != "" {
			v := e.ScheduleID.ValueString()
			a.ScheduleID = &v
		}
		out = append(out, a)
	}
	return out, nil
}

// channelAssignmentsFromAPI converts API assignments to a set value.
func channelAssignmentsFromAPI(ctx context.Context, in []logclient.ChannelAssignment) (types.Set, diag.Diagnostics) {
	elems := make([]channelAssignmentModel, 0, len(in))
	for _, a := range in {
		m := channelAssignmentModel{ChannelID: types.StringValue(a.ChannelID), ScheduleID: types.StringNull()}
		if a.ScheduleID != nil && *a.ScheduleID != "" {
			m.ScheduleID = types.StringValue(*a.ScheduleID)
		}
		elems = append(elems, m)
	}
	return types.SetValueFrom(ctx, channelAssignmentObjectType, elems)
}
