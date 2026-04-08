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
	_ basetypes.StringTypable                    = definitionStringType{}
	_ basetypes.StringValuable                   = definitionStringValue{}
	_ basetypes.StringValuableWithSemanticEquals = definitionStringValue{}
)

type definitionStringType struct {
	basetypes.StringType
}

func (t definitionStringType) Equal(o attr.Type) bool {
	_, ok := o.(definitionStringType)
	return ok
}

func (definitionStringType) String() string {
	return "definitionStringType"
}

func (t definitionStringType) ValueFromString(ctx context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	if in.IsNull() || in.IsUnknown() {
		return definitionStringValue{StringValue: in}, nil
	}

	var diags diag.Diagnostics
	normalized, _, err := normalizeDefinitionString(in.ValueString())
	if err != nil {
		diags.AddError("Invalid dashboard definition", err.Error())
		return definitionStringValue{StringValue: basetypes.NewStringUnknown()}, diags
	}

	return definitionStringValue{
		StringValue: basetypes.NewStringValue(normalized),
		normalized:  normalized,
	}, diags
}

func (t definitionStringType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}

	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", attrValue)
	}

	if stringValue.IsNull() || stringValue.IsUnknown() {
		return definitionStringValue{StringValue: stringValue}, nil
	}

	normalized, _, err := normalizeDefinitionString(stringValue.ValueString())
	if err != nil {
		return nil, fmt.Errorf("invalid dashboard definition: %w", err)
	}

	return definitionStringValue{
		StringValue: stringValue,
		normalized:  normalized,
	}, nil
}

func (t definitionStringType) ValueType(ctx context.Context) attr.Value {
	return definitionStringValue{StringValue: basetypes.NewStringNull()}
}

type definitionStringValue struct {
	basetypes.StringValue
	normalized string
}

func (v definitionStringValue) Equal(o attr.Value) bool {
	other, ok := o.(definitionStringValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

func (definitionStringValue) Type(context.Context) attr.Type {
	return definitionStringType{}
}

func (v definitionStringValue) ToStringValue(ctx context.Context) (basetypes.StringValue, diag.Diagnostics) {
	return v.StringValue, nil
}

func (v definitionStringValue) StringSemanticEquals(ctx context.Context, other basetypes.StringValuable) (bool, diag.Diagnostics) {
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

	otherNormalized, _, err := normalizeDefinitionString(otherString.ValueString())
	if err != nil {
		var diag diag.Diagnostics
		diag.AddError("Invalid dashboard definition", err.Error())
		return false, diag
	}

	selfNormalized := v.normalized
	if selfNormalized == "" {
		selfNormalized, _, err = normalizeDefinitionString(v.ValueString())
		if err != nil {
			var diag diag.Diagnostics
			diag.AddError("Invalid dashboard definition", err.Error())
			return false, diag
		}
	}

	return selfNormalized == otherNormalized, nil
}

func newDefinitionStringValue(value string) definitionStringValue {
	normalized, _, err := normalizeDefinitionString(value)
	if err != nil {
		// Should never happen as API-provided definitions are expected to be valid.
		return definitionStringValue{
			StringValue: basetypes.NewStringValue(value),
			normalized:  value,
		}
	}
	return definitionStringValue{
		StringValue: basetypes.NewStringValue(value),
		normalized:  normalized,
	}
}
