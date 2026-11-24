// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	timeWindowValidator = alertDurationValidator{min: alertTimeWindowMin, max: alertTimeWindowMax}
	frequencyValidator  = alertDurationValidator{min: alertFrequencyMin, max: alertFrequencyMax}
)

func validateDuration(v validator.String, value types.String) diag.Diagnostics {
	resp := &validator.StringResponse{}
	v.ValidateString(context.Background(), validator.StringRequest{
		Path:        path.Root("duration"),
		ConfigValue: value,
	}, resp)
	return resp.Diagnostics
}

func TestAlertDurationValidatorAcceptsAnyDurationInRange(t *testing.T) {
	t.Parallel()

	// The old preset list rejected "20m" and "2h" at terraform validate. The
	// non-canonical spellings are accepted too; alertDurationType keeps them in
	// state instead of diffing on every plan.
	cases := []struct {
		name   string
		v      alertDurationValidator
		inputs []string
	}{
		{
			name: "time_window",
			v:    timeWindowValidator,
			inputs: []string{
				"1m", "2m", "5m", "10m", "15m", "20m", "30m", "47m", "90m",
				"1h", "1h30m", "2h", "6h", "12h", "24h", "2d", "7d", "30d",
			},
		},
		{
			name:   "frequency",
			v:      frequencyValidator,
			inputs: []string{"1m", "3m", "5m", "20m", "30m", "1h", "6h", "12h", "24h"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, input := range tc.inputs {
				t.Run(input, func(t *testing.T) {
					t.Parallel()

					if diags := validateDuration(tc.v, types.StringValue(input)); diags.HasError() {
						t.Fatalf("%s %q was rejected: %v", tc.name, input, diags.Errors())
					}
				})
			}
		})
	}
}

func TestAlertDurationValidatorRejectsOutOfRange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		v     alertDurationValidator
		input string
	}{
		{name: "frequency below minimum", v: frequencyValidator, input: "30s"},
		{name: "frequency above maximum", v: frequencyValidator, input: "7d"},
		{name: "time window below minimum", v: timeWindowValidator, input: "30s"},
		{name: "time window above maximum", v: timeWindowValidator, input: "90d"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			diags := validateDuration(tc.v, types.StringValue(tc.input))
			if !diags.HasError() {
				t.Fatalf("expected %q to be rejected", tc.input)
			}
			if summary := diags.Errors()[0].Summary(); summary != "Duration out of range" {
				t.Fatalf("unexpected summary for %q: %q", tc.input, summary)
			}
		})
	}
}

func TestAlertDurationValidatorRejectsUnparseable(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"not-a-duration", "", "PT20M"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			if !validateDuration(timeWindowValidator, types.StringValue(input)).HasError() {
				t.Fatalf("expected %q to be rejected", input)
			}
		})
	}
}

func TestAlertDurationValidatorSkipsNullAndUnknown(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]types.String{
		"null":    types.StringNull(),
		"unknown": types.StringUnknown(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if diags := validateDuration(timeWindowValidator, value); diags.HasError() {
				t.Fatalf("%s value must not raise an error: %v", name, diags.Errors())
			}
		})
	}
}

func TestAlertDurationCanonicalValuesRoundTripThroughWireFormat(t *testing.T) {
	t.Parallel()

	// A canonical value survives the full provider path:
	// config -> durToISO8601 -> API -> iso8601ToDuration -> durationCompact.
	// Non-canonical spellings do not; alertDurationType keeps them in state.
	for _, input := range []string{"20m", "47m", "2h", "1h30m", "15m", "24h", "7d", "30d"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			d, err := parseDurationText(input)
			if err != nil {
				t.Fatalf("parseDurationText(%q) failed: %v", input, err)
			}

			wire := durToISO8601(d)

			back, err := iso8601ToDuration(wire)
			if err != nil {
				t.Fatalf("iso8601ToDuration(%q) failed: %v", wire, err)
			}

			if got := durationCompact(back); got != input {
				t.Fatalf("%q round-tripped via %q to %q; this would diff on every plan", input, wire, got)
			}
		})
	}
}

// TestAlertSchemaAppliesDurationValidators guards the wiring rather than the
// validator. Every other test here builds an alertDurationValidator directly,
// so all of them would still pass if the schema stopped using it, or used it
// with the wrong bounds. This one pulls the validators off the real resource
// schema and runs them.
func TestAlertSchemaAppliesDurationValidators(t *testing.T) {
	t.Parallel()

	resp := &resource.SchemaResponse{}
	(&AlertResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("building the alert schema failed: %v", resp.Diagnostics.Errors())
	}

	cases := []struct {
		attribute string
		accepts   []string
		rejects   []string
	}{
		{
			attribute: "time_window",
			accepts:   []string{"20m", "2h", "47m", "7d", "30d", "90m"},
			rejects:   []string{"30s", "90d"},
		},
		{
			attribute: "frequency",
			accepts:   []string{"1m", "3m", "20m", "24h", "90m"},
			rejects:   []string{"30s", "7d"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.attribute, func(t *testing.T) {
			t.Parallel()

			attr, ok := resp.Schema.Attributes[tc.attribute].(rschema.StringAttribute)
			if !ok {
				t.Fatalf("%s is not a string attribute", tc.attribute)
			}
			if len(attr.Validators) == 0 {
				t.Fatalf("%s has no validators", tc.attribute)
			}
			if _, ok := attr.CustomType.(alertDurationType); !ok {
				t.Fatalf("%s does not use alertDurationType: %T", tc.attribute, attr.CustomType)
			}

			run := func(input string) diag.Diagnostics {
				var diags diag.Diagnostics
				for _, v := range attr.Validators {
					diags.Append(validateDuration(v, types.StringValue(input))...)
				}
				return diags
			}

			for _, input := range tc.accepts {
				if d := run(input); d.HasError() {
					t.Errorf("schema rejected %s = %q: %v", tc.attribute, input, d.Errors())
				}
			}
			for _, input := range tc.rejects {
				if d := run(input); !d.HasError() {
					t.Errorf("schema accepted %s = %q, expected rejection", tc.attribute, input)
				}
			}
		})
	}
}
