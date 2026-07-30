// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var _ validator.String = thresholdValidator{}

// thresholdRe matches a plain (optionally negative) decimal number in the
// metric's native unit, e.g. "60000" or "1.5". Rejects fractions/scientific
// notation so the value round-trips as a decimal string.
var thresholdRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// thresholdValidator validates a `histogram_threshold` SLO cutoff: a decimal
// number in the metric's native unit.
type thresholdValidator struct{}

func (v thresholdValidator) Description(_ context.Context) string {
	return "must be a decimal number in the metric's native unit"
}

func (v thresholdValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v thresholdValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	raw := strings.TrimSpace(req.ConfigValue.ValueString())
	if !thresholdRe.MatchString(raw) {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid threshold value",
			fmt.Sprintf("threshold must be a decimal number, e.g. \"60000\", got %q", raw),
		)
	}
}

func newThresholdValidator() validator.String {
	return thresholdValidator{}
}
