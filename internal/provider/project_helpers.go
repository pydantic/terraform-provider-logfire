// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// projectMissing reports whether the referenced project no longer exists.
// This is used to make child resources tolerant to parent-first deletion in
// controller-driven flows such as Upjet/Crossplane, without masking ordinary
// permission errors when the project still exists. Only an API answer can mean
// that the project is gone: a request the client refused, such as one with a
// missing ID, stays an error.
func projectMissing(ctx context.Context, c *logclient.APIClient, projectID string, err error) bool {
	var apiErr *logclient.APIError
	if projectID == "" || !errors.As(err, &apiErr) {
		return false
	}
	_, status, err := c.GetProject(ctx, projectID)
	return err != nil && (status == 404 || logclient.IsNotFoundError(err))
}
