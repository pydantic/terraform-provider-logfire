// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var _ validator.String = targetPercentValidator{}

var oneHundred = big.NewRat(100, 1)

// targetPercentValidator validates SLO target percentages: a decimal string
// exclusively between 0 and 100, matching the API contract.
type targetPercentValidator struct{}

func (v targetPercentValidator) Description(_ context.Context) string {
	return "must be a decimal number exclusively between 0 and 100"
}

func (v targetPercentValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v targetPercentValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	raw := strings.TrimSpace(req.ConfigValue.ValueString())
	if !decimalRe.MatchString(raw) {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid target_percent value",
			fmt.Sprintf("target_percent must be a decimal number, e.g. \"99.9\", got %q", raw),
		)
		return
	}

	val, ok := new(big.Rat).SetString(raw)
	if !ok || val.Sign() <= 0 || val.Cmp(oneHundred) >= 0 {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid target_percent value",
			fmt.Sprintf("target_percent must be exclusively between 0 and 100, got %q", raw),
		)
	}
}

func newTargetPercentValidator() validator.String {
	return targetPercentValidator{}
}
