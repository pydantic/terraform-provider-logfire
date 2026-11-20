// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"net/http"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// projectMissing reports whether the referenced project no longer exists, so that
// child resources tolerate parent-first deletion in controller-driven flows such
// as Upjet/Crossplane. Logfire answers a deleted project with 403, the same as a
// project the credentials cannot see, so a 403 counts as missing only when the
// project list also leaves the project out. A project the credentials still list
// keeps its ordinary permission errors.
func projectMissing(ctx context.Context, c *logclient.APIClient, projectID string) bool {
	if c == nil || projectID == "" {
		return false
	}

	_, status, err := c.GetProject(ctx, projectID)
	switch {
	case err == nil:
		return false
	case status == http.StatusNotFound || logclient.IsNotFoundError(err):
		return true
	case status != http.StatusForbidden:
		return false
	}

	projects, err := c.ListProjects(ctx)
	if err != nil {
		return false
	}
	for _, p := range projects {
		if p.ID == projectID {
			return false
		}
	}
	return true
}
