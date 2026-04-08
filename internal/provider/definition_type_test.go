// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestDefinitionStringTypeValueFromTerraformPreservesConfigValue(t *testing.T) {
	t.Parallel()

	raw := `{"kind":"Dashboard","metadata":{"name":"ui-name","project":"test","createdAt":"2026-02-19T12:51:31.131766Z","updatedAt":"2026-02-19T12:53:21.203087Z","version":5},"spec":{"display":{"name":"dash"}}}`

	value, err := definitionStringType{}.ValueFromTerraform(context.Background(), tftypes.NewValue(tftypes.String, raw))
	if err != nil {
		t.Fatalf("ValueFromTerraform returned error: %v", err)
	}

	defValue, ok := value.(definitionStringValue)
	if !ok {
		t.Fatalf("unexpected value type %T", value)
	}

	if defValue.ValueString() != raw {
		t.Fatalf("config value must be preserved exactly")
	}
}

func TestDefinitionStringSemanticEqualsIgnoresMetadataOnlyChanges(t *testing.T) {
	t.Parallel()

	withMetadata := newDefinitionStringValue(`{"kind":"Dashboard","metadata":{"name":"ui-name","project":"test","createdAt":"2026-02-19T12:51:31.131766Z","updatedAt":"2026-02-19T12:53:21.203087Z","version":5},"spec":{"display":{"name":"dash"}}}`)
	withoutMetadata := newDefinitionStringValue(`{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"dash"}}}`)

	equal, diags := withMetadata.StringSemanticEquals(context.Background(), withoutMetadata)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !equal {
		t.Fatalf("expected values to be semantically equal")
	}
}

func TestDefinitionStringSemanticEqualsDetectsMeaningfulChanges(t *testing.T) {
	t.Parallel()

	first := newDefinitionStringValue(`{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"dash-a"}}}`)
	second := newDefinitionStringValue(`{"kind":"Dashboard","metadata":{},"spec":{"display":{"name":"dash-b"}}}`)

	equal, diags := first.StringSemanticEquals(context.Background(), second)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if equal {
		t.Fatalf("expected values to be different when spec changes")
	}
}

func TestDefinitionStringValueEqualUsesUnderlyingStringValue(t *testing.T) {
	t.Parallel()

	first := newDefinitionStringValue(`{"kind":"Dashboard","metadata":{"name":"dash-a"},"spec":{"display":{"name":"dash-a"}}}`)
	second := newDefinitionStringValue(`{"kind":"Dashboard","metadata":{"name":"dash-b"},"spec":{"display":{"name":"dash-b"}}}`)

	if first.Equal(second) {
		t.Fatalf("expected Equal to compare the underlying string value, not semantic equality")
	}

	if !first.Equal(newDefinitionStringValue(first.ValueString())) {
		t.Fatalf("expected Equal to match identical underlying string values")
	}

	if !(definitionStringValue{StringValue: basetypes.NewStringNull()}).Equal(definitionStringValue{StringValue: basetypes.NewStringNull()}) {
		t.Fatalf("expected null values to compare equal")
	}
}
