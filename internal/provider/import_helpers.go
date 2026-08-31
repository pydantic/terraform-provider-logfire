// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	client "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const importSeparators = "/,|"

func splitImportParts(raw string, allowedCounts ...int) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("expected a non-empty import ID")
	}
	var parts []string
	segmentStart := 0
	for i, r := range trimmed {
		if !strings.ContainsRune(importSeparators, r) {
			continue
		}
		segment := strings.TrimSpace(trimmed[segmentStart:i])
		if segment == "" {
			return nil, fmt.Errorf("import ID contains empty segment")
		}
		parts = append(parts, segment)
		segmentStart = i + 1
	}
	segment := strings.TrimSpace(trimmed[segmentStart:])
	if segment == "" {
		return nil, fmt.Errorf("import ID contains empty segment")
	}
	parts = append(parts, segment)
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
