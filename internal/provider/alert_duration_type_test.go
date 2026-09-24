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

	cases := []struct {
		name  string
		prior string
		next  string
		want  bool
	}{
		{name: "same spelling", prior: "20m", next: "20m", want: true},
		{name: "minutes as hours and minutes", prior: "90m", next: "1h30m", want: true},
		{name: "hours and minutes as minutes", prior: "1h30m", next: "90m", want: true},
		{name: "days as hours", prior: "2d", next: "48h", want: true},
		{name: "hours as days", prior: "48h", next: "2d", want: true},
		{name: "seconds as minutes", prior: "120s", next: "2m", want: true},
		{name: "week as hours", prior: "7d", next: "168h", want: true},
		{name: "different durations", prior: "20m", next: "30m", want: false},
		{name: "hour against day", prior: "24h", next: "2d", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			equal, diags := newAlertDurationValue(tc.prior).StringSemanticEquals(context.Background(), newAlertDurationValue(tc.next))
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if equal != tc.want {
				t.Fatalf("StringSemanticEquals(%q, %q) = %v, want %v", tc.prior, tc.next, equal, tc.want)
			}
		})
	}
}

func TestAlertDurationSemanticEqualsNullAndUnknown(t *testing.T) {
	t.Parallel()

	nullValue := alertDurationValue{StringValue: basetypes.NewStringNull()}
	unknownValue := alertDurationValue{StringValue: basetypes.NewStringUnknown()}

	cases := []struct {
		name  string
		prior alertDurationValue
		next  alertDurationValue
		want  bool
	}{
		{name: "null equals null", prior: nullValue, next: nullValue, want: true},
		{name: "unknown equals unknown", prior: unknownValue, next: unknownValue, want: true},
		{name: "null against a value", prior: nullValue, next: newAlertDurationValue("20m"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			equal, diags := tc.prior.StringSemanticEquals(context.Background(), tc.next)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if equal != tc.want {
				t.Fatalf("StringSemanticEquals = %v, want %v", equal, tc.want)
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
