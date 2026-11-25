// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestAlertDurationTypeValueFromTerraformPreservesConfigValue(t *testing.T) {
	t.Parallel()

	value, err := alertDurationType{}.ValueFromTerraform(context.Background(), tftypes.NewValue(tftypes.String, "90m"))
	if err != nil {
		t.Fatalf("ValueFromTerraform returned error: %v", err)
	}

	durationValue, ok := value.(alertDurationValue)
	if !ok {
		t.Fatalf("unexpected value type %T", value)
	}
	if durationValue.ValueString() != "90m" {
		t.Fatalf("config value must be preserved exactly, got %q", durationValue.ValueString())
	}
}

func TestAlertDurationSemanticEquals(t *testing.T) {
	t.Parallel()

	dur := newAlertDurationValue
	nullValue := alertDurationValue{StringValue: basetypes.NewStringNull()}
	unknownValue := alertDurationValue{StringValue: basetypes.NewStringUnknown()}

	cases := []struct {
		name  string
		prior alertDurationValue
		next  alertDurationValue
		want  bool
	}{
		{name: "same spelling", prior: dur("20m"), next: dur("20m"), want: true},
		{name: "minutes as hours and minutes", prior: dur("90m"), next: dur("1h30m"), want: true},
		{name: "hours and minutes as minutes", prior: dur("1h30m"), next: dur("90m"), want: true},
		{name: "days as hours", prior: dur("2d"), next: dur("48h"), want: true},
		{name: "hours as days", prior: dur("48h"), next: dur("2d"), want: true},
		{name: "seconds as minutes", prior: dur("120s"), next: dur("2m"), want: true},
		{name: "week as hours", prior: dur("7d"), next: dur("168h"), want: true},
		{name: "different durations", prior: dur("20m"), next: dur("30m"), want: false},
		{name: "hour against day", prior: dur("24h"), next: dur("2d"), want: false},
		{name: "null equals null", prior: nullValue, next: nullValue, want: true},
		{name: "unknown equals unknown", prior: unknownValue, next: unknownValue, want: true},
		{name: "null against a value", prior: nullValue, next: dur("20m"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			equal, diags := tc.prior.StringSemanticEquals(context.Background(), tc.next)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if equal != tc.want {
				t.Fatalf("StringSemanticEquals(%v, %v) = %v, want %v", tc.prior, tc.next, equal, tc.want)
			}
		})
	}
}

func TestAlertDurationValueEqualUsesUnderlyingStringValue(t *testing.T) {
	t.Parallel()

	if !newAlertDurationValue("20m").Equal(newAlertDurationValue("20m")) {
		t.Fatalf("identical spellings must be equal")
	}
	if newAlertDurationValue("90m").Equal(newAlertDurationValue("1h30m")) {
		t.Fatalf("Equal must compare the underlying string exactly, not semantically")
	}
}
