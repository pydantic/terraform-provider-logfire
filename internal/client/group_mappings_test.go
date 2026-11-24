// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func groupMappingsClient(t *testing.T, status int, body string, check func(*http.Request)) *APIClient {
	t.Helper()
	c, err := NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/api/v1/instance/group-mappings/" {
				t.Fatalf("request path = %s, want the instance route and no token exchange", req.URL.Path)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer admin-key" {
				t.Fatalf("Authorization = %q, want the raw key", got)
			}
			if check != nil {
				check(req)
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Logfire-Version": []string{"v2026-06-01.01"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetGroupMappings(t *testing.T) {
	t.Parallel()
	c := groupMappingsClient(t, http.StatusOK, `{"group_organization_mapping":[{"group_id":"ops","organization_roles":[
		{"organization_name":"team-a","role":"guest","project_roles":[{"project_name":"api","role":"read"}]}]}],
		"api_mapping_status":"overridden_by_environment"}`,
		func(req *http.Request) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s", req.Method)
			}
		})
	out, err := c.GetGroupMappings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.APIMappingStatus != GroupMappingsStatusOverriddenByEnvironment {
		t.Errorf("status = %q", out.APIMappingStatus)
	}
	role := out.GroupOrganizationMapping[0].OrganizationRoles[0]
	if role.OrganizationName != "team-a" || role.ProjectRoles[0].ProjectName != "api" {
		t.Errorf("decoded %+v", role)
	}
}

func TestGetGroupMappingsRouteMissing(t *testing.T) {
	t.Parallel()
	c := groupMappingsClient(t, http.StatusNotFound, `{"detail":"Not Found"}`, nil)
	_, err := c.GetGroupMappings(context.Background())
	var unavailable *EndpointUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want EndpointUnavailableError", err)
	}
	for _, want := range []string{"v2026-07-22.02", "logfire-0.13.36", "v2026-06-01.01"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not name %s", err, want)
		}
	}
}

// An instance from before api_mapping_status cannot say whether the Helm value overrides
// the API, and its write has no 409, so the client refuses to guess.
func TestGetGroupMappingsWithoutStatus(t *testing.T) {
	t.Parallel()
	c := groupMappingsClient(t, http.StatusOK, `{"group_organization_mapping":[]}`, nil)
	_, err := c.GetGroupMappings(context.Background())
	if err == nil || !strings.Contains(err.Error(), "v2026-07-22.02") {
		t.Fatalf("error = %v, want the release floor", err)
	}
}

func TestSetGroupMappingsSendsListEvenWhenEmpty(t *testing.T) {
	t.Parallel()
	var body string
	c := groupMappingsClient(t, http.StatusOK, `{"group_organization_mapping":[]}`, func(req *http.Request) {
		if req.Method != http.MethodPut {
			t.Fatalf("method = %s", req.Method)
		}
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	})
	stored, err := c.SetGroupMappings(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if body != `{"group_organization_mapping":[]}` {
		t.Errorf("body = %s", body)
	}
	if stored == nil || len(stored) != 0 {
		t.Errorf("stored = %#v, want an empty list", stored)
	}
}

func TestSetGroupMappingsErrors(t *testing.T) {
	t.Parallel()
	t.Run("409 is the override", func(t *testing.T) {
		t.Parallel()
		c := groupMappingsClient(t, http.StatusConflict, `{"detail":"environment"}`, nil)
		if _, err := c.SetGroupMappings(context.Background(), nil); !errors.Is(err, ErrGroupMappingsOverridden) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("400 carries the detail", func(t *testing.T) {
		t.Parallel()
		c := groupMappingsClient(t, http.StatusBadRequest, `{"detail":"Organization 'x' does not exist"}`, nil)
		_, err := c.SetGroupMappings(context.Background(), nil)
		var invalid *GroupMappingsInvalidError
		if !errors.As(err, &invalid) || invalid.Detail != "Organization 'x' does not exist" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("400 without a JSON body keeps the text", func(t *testing.T) {
		t.Parallel()
		c := groupMappingsClient(t, http.StatusBadRequest, `plain refusal`, nil)
		_, err := c.SetGroupMappings(context.Background(), nil)
		var invalid *GroupMappingsInvalidError
		if !errors.As(err, &invalid) || invalid.Detail != "plain refusal" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("other statuses stay API errors", func(t *testing.T) {
		t.Parallel()
		c := groupMappingsClient(t, http.StatusForbidden, `{"detail":"This token is missing the required scope: instance:admin."}`, nil)
		_, err := c.SetGroupMappings(context.Background(), nil)
		if !strings.Contains(err.Error(), "status 403") {
			t.Fatalf("error = %v", err)
		}
	})
}

// The instance always sends the list, so an answer without it fails instead of
// reading as an empty mapping.
func TestGroupMappingsAnswerWithoutTheList(t *testing.T) {
	t.Parallel()
	get := groupMappingsClient(t, http.StatusOK, `{"api_mapping_status":"active"}`, nil)
	if _, err := get.GetGroupMappings(context.Background()); err == nil || !strings.Contains(err.Error(), "has no group_organization_mapping") {
		t.Errorf("GET err = %v, want the missing list reported", err)
	}
	put := groupMappingsClient(t, http.StatusOK, `{}`, nil)
	if _, err := put.SetGroupMappings(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "has no group_organization_mapping") {
		t.Errorf("PUT err = %v, want the missing list reported", err)
	}
}
