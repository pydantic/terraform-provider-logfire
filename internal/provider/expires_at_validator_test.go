// Copyright Pydantic, Inc. 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestOptionalRFC3339Validator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   types.String
		wantErr bool
	}{
		{name: "null allowed", input: types.StringNull()},
		{name: "unknown allowed", input: types.StringUnknown()},
		{name: "empty allowed", input: types.StringValue("")},
		{name: "whitespace allowed", input: types.StringValue("   ")},
		{name: "rfc3339 nano zulu", input: types.StringValue("2026-03-02T12:34:56.789Z")},
		{name: "rfc3339 offset", input: types.StringValue("2026-03-02T12:34:56-07:00")},
		{name: "invalid date", input: types.StringValue("2026-03-02"), wantErr: true},
	}

	v := newOptionalRFC3339Validator()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := validator.StringRequest{
				Path:        path.Root("expires_at"),
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
