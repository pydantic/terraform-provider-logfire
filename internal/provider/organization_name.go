// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// organizationNamePattern is the instance's rule for organization names. The
// token exchange reads the audience <base_url>/<organization> as a path, so a name
// outside the rule, such as "team-a/", would act in another organization.
var organizationNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const organizationNameRule = "must use lowercase letters, numbers, and internal hyphens (no spaces or uppercase characters)"

func organizationNameValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtLeast(2),
		stringvalidator.RegexMatches(organizationNamePattern, organizationNameRule),
	}
}
