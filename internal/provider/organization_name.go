// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
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

// rejectOrganizationContext stops a resource that needs a user behind its
// credential. The exchanged token of an organization has none, and a refresh
// through it does not see personal keys, so it would drop them from state.
func rejectOrganizationContext(c *logclient.APIClient, diags *diag.Diagnostics) bool {
	if c.Organization() == "" {
		return false
	}
	diags.AddError("Not available through an organization", fmt.Sprintf(
		"This resource needs a credential with a user, but the provider acts in "+
			"organization %q through a token that has none. Use a provider without "+
			"organization, with an API key of that organization.", c.Organization()))
	return true
}
