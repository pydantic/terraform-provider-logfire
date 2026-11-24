// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const groupMappingsPath = "/api/v1/instance/group-mappings/"

const (
	// GroupMappingsStatusActive means the stored mapping is the one that
	// sign-in uses.
	GroupMappingsStatusActive = "active"
	// GroupMappingsStatusOverriddenByEnvironment means the Helm value
	// groupOrganizationMapping is set, so the stored mapping is not used and
	// the API refuses writes.
	GroupMappingsStatusOverriddenByEnvironment = "overridden_by_environment"
)

// The instance answers a route this old without api_mapping_status and
// without the 409 on write, so the provider cannot tell whether the Helm value
// overrides the API.
const (
	groupMappingsMinimumRelease = "v2026-07-22.02"
	groupMappingsMinimumChart   = "logfire-0.13.36"
)

type GroupMappingProjectRole struct {
	ProjectName string `json:"project_name"`
	Role        string `json:"role"`
}

type GroupMappingOrganizationRole struct {
	OrganizationName string                    `json:"organization_name"`
	Role             string                    `json:"role"`
	ProjectRoles     []GroupMappingProjectRole `json:"project_roles"`
}

type GroupMapping struct {
	GroupID           string                         `json:"group_id"`
	OrganizationRoles []GroupMappingOrganizationRole `json:"organization_roles"`
}

type GroupMappingsRead struct {
	GroupOrganizationMapping []GroupMapping `json:"group_organization_mapping"`
	APIMappingStatus         string         `json:"api_mapping_status"`
}

// groupMappingsAnswer decodes a read or write answer. The instance always sends
// the list, so a nil pointer means the answer came from something else, such as
// a proxy.
type groupMappingsAnswer struct {
	GroupOrganizationMapping *[]GroupMapping `json:"group_organization_mapping"`
	APIMappingStatus         string          `json:"api_mapping_status"`
}

func errGroupMappingsMissing(method string) error {
	return fmt.Errorf("the answer to %s %s has no group_organization_mapping, "+
		"so it did not come from the Logfire API. Check that base_url reaches "+
		"the instance without a proxy or redirect that rewrites it",
		method, groupMappingsPath)
}

// ErrGroupMappingsOverridden is the 409 that a write gets while the Helm value
// groupOrganizationMapping overrides the API.
var ErrGroupMappingsOverridden = errors.New(
	"the group mapping is configured through the environment")

// GroupMappingsInvalidError is the 400 for a mapping that names a missing
// organization or project, or repeats a group ID.
type GroupMappingsInvalidError struct {
	Detail string
}

func (e *GroupMappingsInvalidError) Error() string { return e.Detail }

// GetGroupMappings reads the stored mapping and whether the Helm value
// overrides it.
func (c *APIClient) GetGroupMappings(ctx context.Context) (*GroupMappingsRead, error) {
	out, err := request[groupMappingsAnswer](
		ctx, c, http.MethodGet, groupMappingsPath, nil)
	if err != nil {
		// The collection route has no item ID, so a 404 means that the route is
		// missing.
		return nil, unavailableIfNotFound(err, http.MethodGet,
			groupMappingsPath, groupMappingsMinimumRelease,
			groupMappingsMinimumChart)
	}
	if out.APIMappingStatus == "" {
		return nil, fmt.Errorf("this Logfire instance does not report "+
			"api_mapping_status, so the provider cannot tell whether the Helm "+
			"value groupOrganizationMapping overrides the API. This requires "+
			"Logfire %s or newer (Helm chart %s or newer). Upgrade the "+
			"instance, or manage the group mapping with the Helm value until "+
			"then",
			groupMappingsMinimumRelease, groupMappingsMinimumChart)
	}
	if out.GroupOrganizationMapping == nil {
		return nil, errGroupMappingsMissing(http.MethodGet)
	}
	return &GroupMappingsRead{
		GroupOrganizationMapping: *out.GroupOrganizationMapping,
		APIMappingStatus:         out.APIMappingStatus,
	}, nil
}

// SetGroupMappings replaces the whole stored mapping and returns what the
// instance stored. The write is idempotent, so the client's automatic retries
// apply.
func (c *APIClient) SetGroupMappings(ctx context.Context, mapping []GroupMapping) ([]GroupMapping, error) {
	if mapping == nil {
		mapping = []GroupMapping{}
	}
	body := map[string][]GroupMapping{"group_organization_mapping": mapping}
	out, err := request[groupMappingsAnswer](
		ctx, c, http.MethodPut, groupMappingsPath, body)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusConflict:
			return nil, ErrGroupMappingsOverridden
		case http.StatusBadRequest:
			// The detail is the FastAPI detail string, or else the raw body.
			detail := apiErr.Message
			var answer struct {
				Detail string `json:"detail"`
			}
			if json.Unmarshal([]byte(detail), &answer) == nil &&
				answer.Detail != "" {
				detail = answer.Detail
			}
			return nil, &GroupMappingsInvalidError{Detail: detail}
		}
	}
	if err != nil {
		return nil, err
	}
	if out.GroupOrganizationMapping == nil {
		return nil, errGroupMappingsMissing(http.MethodPut)
	}
	return *out.GroupOrganizationMapping, nil
}
