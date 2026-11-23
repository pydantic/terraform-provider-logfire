// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

const (
	membersPath          = "/api/v1/members/"
	memberNotFoundDetail = "Organization member not found"
)

// OrganizationMember is a member of the organization that the credential belongs to.
// Role is the role's name, which is a custom name when someone set one in the UI.
type OrganizationMember struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	RoleID      string `json:"role_id"`
	Role        string `json:"role"`
	MemberSince string `json:"member_since"`
}

// OrganizationMemberUpdate is the body of a role change. The API accepts only the
// built-in roles admin, member, guest and billing.
type OrganizationMemberUpdate struct {
	Role string `json:"role"`
}

func memberPath(userID string) string {
	return membersPath + url.PathEscape(userID) + "/"
}

// ListMembers returns every member of the organization. The route has no pagination.
func (c *APIClient) ListMembers(ctx context.Context) ([]OrganizationMember, error) {
	var out []OrganizationMember
	if _, err := c.doJSON(ctx, http.MethodGet, membersPath, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateMemberRole sets the role of a member. The route answers 204 with no body.
func (c *APIClient) UpdateMemberRole(ctx context.Context, userID, role string) error {
	_, err := c.doJSON(ctx, http.MethodPatch, memberPath(userID), OrganizationMemberUpdate{Role: role}, nil, http.StatusNoContent)
	return err
}

// DeleteMember removes a member from the organization. The user keeps the account.
func (c *APIClient) DeleteMember(ctx context.Context, userID string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, memberPath(userID), nil, nil, http.StatusNoContent)
	return err
}

// IsMemberNotFoundError reports the member routes' answer for a user who is not a member.
// A 404 with another detail comes from a missing route, as on an older backend, and says
// nothing about the member.
func IsMemberNotFoundError(err error) bool {
	status, detail, ok := apiErrorStatusDetail(err)
	return ok && status == http.StatusNotFound && detail == memberNotFoundDetail
}

// MemberRequestError describes a refused member request in the words of the member
// routes. The routes share a few status codes that each mean one thing there.
func MemberRequestError(action string, err error) error {
	status, detail, ok := apiErrorStatusDetail(err)
	if !ok {
		return fmt.Errorf("%s: %w", action, err)
	}
	var hint string
	switch status {
	case http.StatusBadRequest:
		hint = "Logfire requires more than one admin in an organization. Make another member an admin first."
	case http.StatusPaymentRequired:
		hint = "The organization's plan has no free seat for this role."
	case http.StatusForbidden:
		hint = "The credential cannot grant a role with more permissions than its own or remove a member who has more, " +
			"and it needs the `organization:write_member` scope."
	case http.StatusNotFound:
		hint = "Logfire did not find the member or the role, or this backend has no member route."
	case http.StatusUnprocessableEntity:
		hint = "Logfire does not allow this change for this member or organization."
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %s (HTTP %d: %s)", action, hint, status, detail)
}
