// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	client "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// splitImportParts splits an import ID at "/", "," or "|" and trims each
// segment. With allowedCounts, the segment count must be one of them.
func splitImportParts(raw string, allowedCounts ...int) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("expected a non-empty import ID")
	}
	parts := strings.Split(strings.NewReplacer("/", ",", "|", ",").Replace(trimmed), ",")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
		if parts[i] == "" {
			return nil, fmt.Errorf("import ID contains empty segment")
		}
	}
	if len(allowedCounts) > 0 && !slices.Contains(allowedCounts, len(parts)) {
		return nil, fmt.Errorf("invalid import ID segment count %d", len(parts))
	}
	return parts, nil
}

// projectNotFoundError means the project list answered and has no such project.
type projectNotFoundError struct{ key string }

func (e *projectNotFoundError) Error() string { return fmt.Sprintf("project %q not found", e.key) }

func findProjectByNameOrID(ctx context.Context, c *client.APIClient, key string) (string, string, error) {
	list, err := c.ListProjects(ctx)
	if err != nil {
		return "", "", err
	}
	for _, project := range list {
		if project.ID == key {
			return project.ID, project.ProjectName, nil
		}
	}
	for _, project := range list {
		if project.ProjectName == key {
			return project.ID, project.ProjectName, nil
		}
	}
	return "", "", &projectNotFoundError{key: key}
}
