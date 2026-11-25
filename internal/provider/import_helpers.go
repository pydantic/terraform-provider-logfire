// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"

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

// projectImport describes a resource imported as "project/item". The project
// is found by ID or name, then the item in the project's list by ID or name.
type projectImport[T any] struct {
	noun    string
	counts  []int
	missing string
	invalid string
	list    func(ctx context.Context, projectID string) ([]T, error)
	keys    func(*T) (id, name string)
	// duplicate is the warning format (name key, project name) for several
	// name matches; empty imports the first one silently.
	duplicate string
}

// importProjectItem resolves req.ID to the project and the list item it names.
// The last ID segment is the name key, so a longer form can name the item by
// something other than the second segment. On failure it reports the
// diagnostics and returns false.
func importProjectItem[T any](ctx context.Context, c *client.APIClient, req resource.ImportStateRequest, resp *resource.ImportStateResponse, spec projectImport[T]) (string, *T, bool) {
	if strings.TrimSpace(req.ID) == "" {
		resp.Diagnostics.AddError("Missing import ID", spec.missing)
		return "", nil, false
	}
	parts, err := splitImportParts(req.ID, spec.counts...)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", spec.invalid)
		return "", nil, false
	}

	failed := "Import " + spec.noun + " failed"
	projectID, projectName, err := findProjectByNameOrID(ctx, c, parts[0])
	if err != nil {
		resp.Diagnostics.AddError(failed, err.Error())
		return "", nil, false
	}
	items, err := spec.list(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError(failed, fmt.Sprintf("listing %ss for project %q: %v", spec.noun, projectID, err))
		return "", nil, false
	}

	key, nameKey := parts[1], parts[len(parts)-1]
	var byName []*T
	for i := range items {
		id, name := spec.keys(&items[i])
		if id == key {
			return projectID, &items[i], true
		}
		if name == nameKey {
			byName = append(byName, &items[i])
		}
	}
	if len(byName) == 0 {
		resp.Diagnostics.AddError(failed, fmt.Sprintf("%s %q not found in project %q", spec.noun, key, projectName))
		return "", nil, false
	}
	if len(byName) > 1 && spec.duplicate != "" {
		resp.Diagnostics.AddWarning("Import "+spec.noun, fmt.Sprintf(spec.duplicate, nameKey, projectName))
	}
	return projectID, byName[0], true
}
