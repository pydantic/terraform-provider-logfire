// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var (
	_ basetypes.StringTypable                    = alertDurationType{}
	_ basetypes.StringValuable                   = alertDurationValue{}
	_ basetypes.StringValuableWithSemanticEquals = alertDurationValue{}
)

// alertDurationType keeps the spelling a user writes when a new value means
// the same duration. Read rewrites the attribute to its compact form, so
// without semantic equality 90m for 1h30m would diff on every plan. It mirrors
// definitionStringType for dashboard definitions.
type alertDurationType struct {
	basetypes.StringType
}

func (t alertDurationType) Equal(o attr.Type) bool {
	_, ok := o.(alertDurationType)
	return ok
}

func (alertDurationType) String() string {
	return "alertDurationType"
}

func (t alertDurationType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return alertDurationValue{StringValue: in}, nil
}

func (t alertDurationType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}

	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", attrValue)
	}

	return alertDurationValue{StringValue: stringValue}, nil
}

func (t alertDurationType) ValueType(context.Context) attr.Value {
	return alertDurationValue{StringValue: basetypes.NewStringNull()}
}

type alertDurationValue struct {
	basetypes.StringValue
}

func (v alertDurationValue) Equal(o attr.Value) bool {
	other, ok := o.(alertDurationValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

func (alertDurationValue) Type(context.Context) attr.Type {
	return alertDurationType{}
}

func (v alertDurationValue) ToStringValue(context.Context) (basetypes.StringValue, diag.Diagnostics) {
	return v.StringValue, nil
}

// StringSemanticEquals compares the durations, so the framework keeps the
// prior value when both spellings mean the same time.
func (v alertDurationValue) StringSemanticEquals(ctx context.Context, other basetypes.StringValuable) (bool, diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return other.IsNull() || other.IsUnknown(), nil
	}

	otherString, diags := other.ToStringValue(ctx)
	if diags.HasError() {
		return false, diags
	}
	if otherString.IsNull() || otherString.IsUnknown() {
		return false, nil
	}

	prior, err := parseDurationText(v.ValueString())
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Invalid duration", err.Error())
		return false, diags
	}

	next, err := parseDurationText(otherString.ValueString())
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Invalid duration", err.Error())
		return false, diags
	}

	return prior == next, nil
}

func newAlertDurationValue(raw string) alertDurationValue {
	return alertDurationValue{StringValue: basetypes.NewStringValue(raw)}
}
