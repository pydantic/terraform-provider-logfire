// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// NewWriteTokenResource returns the write token resource, which shares its
// implementation with the read token.
func NewWriteTokenResource() resource.Resource {
	return &tokenResource{
		kind: "write",
		create: func(ctx context.Context, c *logclient.APIClient,
			projectID string, expiresAt *string) (*logclient.ReadToken, error) {
			out, err := c.CreateWriteToken(ctx, projectID,
				logclient.CreateWriteTokenInput{ExpiresAt: expiresAt})
			if err != nil {
				return nil, err
			}
			token := logclient.ReadToken(*out)
			return &token, nil
		},
		find: func(ctx context.Context, c *logclient.APIClient,
			projectID, tokenID string) (*logclient.ReadToken, error) {
			items, err := c.ListWriteTokens(ctx, projectID)
			return findToken(items, tokenID), err
		},
		remove: func(ctx context.Context, c *logclient.APIClient,
			projectID, tokenID string) error {
			return c.DeleteWriteToken(ctx, projectID, tokenID)
		},
	}
}
