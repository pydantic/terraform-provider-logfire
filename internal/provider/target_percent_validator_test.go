// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestTargetPercentValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   types.String
		wantErr bool
	}{
		{name: "null allowed", input: types.StringNull()},
		{name: "unknown allowed", input: types.StringUnknown()},
		{name: "typical target", input: types.StringValue("99.9")},
		{name: "integer target", input: types.StringValue("99")},
		{name: "low target", input: types.StringValue("0.1")},
		{name: "high precision", input: types.StringValue("99.9999")},
		{name: "zero rejected", input: types.StringValue("0"), wantErr: true},
		{name: "hundred rejected", input: types.StringValue("100"), wantErr: true},
		{name: "over hundred rejected", input: types.StringValue("150.5"), wantErr: true},
		{name: "negative rejected", input: types.StringValue("-1"), wantErr: true},
		{name: "not a number", input: types.StringValue("ninety"), wantErr: true},
		{name: "empty rejected", input: types.StringValue(""), wantErr: true},
	}

	v := newTargetPercentValidator()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := validator.StringRequest{
				Path:        path.Root("target_percent"),
				ConfigValue: tc.input,
			}
			var resp validator.StringResponse
			v.ValidateString(context.Background(), req, &resp)

			if tc.wantErr && !resp.Diagnostics.HasError() {
				t.Fatalf("expected validation error, got none")
			}
			if !tc.wantErr && resp.Diagnostics.HasError() {
				t.Fatalf("unexpected validation error: %v", resp.Diagnostics)
			}
		})
	}
}
