// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var _ validator.String = optionalRFC3339Validator{}

// optionalRFC3339Validator validates optional timestamp strings.
// Null and unknown values are accepted; null represents "no expiration".
type optionalRFC3339Validator struct{}

func (v optionalRFC3339Validator) Description(_ context.Context) string {
	return "must be null or a valid RFC3339 timestamp"
}

func (v optionalRFC3339Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v optionalRFC3339Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	raw := strings.TrimSpace(req.ConfigValue.ValueString())
	if raw == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid expires_at value",
			"expires_at must be null or a valid RFC3339 timestamp. Omit expires_at or set it to null for no expiration.")
		return
	}

	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid expires_at value",
			fmt.Sprintf("expires_at must be null or a valid RFC3339 timestamp: %v", err),
		)
	}
}

func newOptionalRFC3339Validator() validator.String {
	return optionalRFC3339Validator{}
}
