// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// projectMissing reports whether the referenced project no longer exists.
// This is used to make child resources tolerant to parent-first deletion in
// controller-driven flows such as Upjet/Crossplane, without masking ordinary
// permission errors when the project still exists.
func projectMissing(ctx context.Context, c *logclient.APIClient, projectID string) bool {
	if c == nil || projectID == "" {
		return false
	}

	_, status, err := c.GetProject(ctx, projectID)
	if err == nil {
		return false
	}

	return status == 404 || logclient.IsNotFoundError(err)
}
