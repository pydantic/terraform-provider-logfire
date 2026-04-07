// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	client "github.com/pydantic/terraform-provider-logfire/internal/client"
)

var importSeps = []rune{'/', ',', '|'}

func splitImportParts(raw string, allowedCounts ...int) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("expected a non-empty import ID")
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		for _, sep := range importSeps {
			if r == sep {
				return true
			}
		}
		return false
	})
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			return nil, fmt.Errorf("import ID contains empty segment")
		}
	}
	if len(allowedCounts) > 0 {
		for _, n := range allowedCounts {
			if len(parts) == n {
				return parts, nil
			}
		}
		return nil, fmt.Errorf("invalid import ID segment count %d", len(parts))
	}
	return parts, nil
}

func findProjectByNameOrID(ctx context.Context, c *client.APIClient, key string) (string, string, error) {
	list, err := c.ListProjects(ctx)
	if err != nil {
		return "", "", err
	}
	for _, project := range list {
		if project.ID == key || project.ProjectName == key {
			return project.ID, project.ProjectName, nil
		}
	}
	return "", "", fmt.Errorf("project %q not found", key)
}
