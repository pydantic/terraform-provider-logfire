// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// tokenExchangePath is the OAuth token endpoint backing the RFC 8693
	// instance-admin exchange that mints short-lived org-scoped credentials.
	tokenExchangePath = "/api/oauth/token"

	// organizationContextScopes are the scopes the exchanged org-context
	// credential requests: `organization:read` for the org read route,
	// `organization:write` for update and delete.
	organizationContextScopes = "organization:read organization:write"

	// exchangeExpiresInSeconds mirrors the backend automation example; the
	// server caps the value at its configured JWT max age.
	exchangeExpiresInSeconds = 900

	// tokenExpiryMargin shaves the cached credential's validity so a token is
	// never used seconds before its server-side expiry.
	tokenExpiryMargin = 30 * time.Second

	// organizationContextPath is the org-context route namespace: the org is
	// resolved from the token, not from the path.
	organizationContextPath = "/api/v1/organization/"
)

// OAuthTokenError is an RFC 6749 flat-envelope error from the token endpoint
// ({"error": ..., "error_description": ...}). `invalid_target` means the
// audience names no existing organization.
type OAuthTokenError struct {
	StatusCode  int
	Code        string
	Description string
}

func (e *OAuthTokenError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("token exchange failed (%s): %s", e.Code, e.Description)
	}
	return fmt.Sprintf("token exchange failed (%s)", e.Code)
}

// IsInvalidTargetError reports whether the error is a token-exchange rejection
// whose audience names no existing organization. For a managed organization
// that means it no longer exists.
func IsInvalidTargetError(err error) bool {
	var oauthErr *OAuthTokenError
	return errors.As(err, &oauthErr) && oauthErr.Code == "invalid_target"
}

type orgTokenEntry struct {
	token   string
	expires time.Time
}

type tokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// organizationContextToken returns a short-lived org-scoped credential for
// orgName, exchanging the configured instance-admin credential via the OAuth
// token endpoint and caching the result until shortly before its expiry.
// force bypasses the cache, for a retry after an unexpected 401.
func (c *APIClient) organizationContextToken(ctx context.Context, orgName string, force bool) (string, error) {
	c.orgTokenMu.Lock()
	defer c.orgTokenMu.Unlock()

	if !force {
		if entry, ok := c.orgTokenCache[orgName]; ok && time.Now().Before(entry.expires) {
			return entry.token, nil
		}
	}

	form := url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {c.token},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"audience":           {strings.TrimSuffix(c.BaseURL.String(), "/") + "/" + orgName},
		"scope":              {organizationContextScopes},
		"expires_in":         {fmt.Sprintf("%d", exchangeExpiresInSeconds)},
	}
	var out tokenExchangeResponse
	_, err := c.doFormJSON(ctx, tokenExchangePath, form, &out, http.StatusOK)
	if err != nil {
		return "", asOAuthTokenError(err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token exchange returned no access token")
	}
	valid := time.Duration(out.ExpiresIn) * time.Second
	if valid <= 0 {
		valid = exchangeExpiresInSeconds * time.Second
	}
	if valid > 2*tokenExpiryMargin {
		valid -= tokenExpiryMargin
	} else {
		valid /= 2
	}
	c.orgTokenCache[orgName] = orgTokenEntry{token: out.AccessToken, expires: time.Now().Add(valid)}
	return out.AccessToken, nil
}

// asOAuthTokenError converts a token-endpoint APIError into an OAuthTokenError
// when the body carries the RFC 6749 flat envelope.
func asOAuthTokenError(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	var envelope struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if jsonErr := json.Unmarshal([]byte(apiErr.Message), &envelope); jsonErr != nil || envelope.Error == "" {
		return err
	}
	return &OAuthTokenError{
		StatusCode:  apiErr.StatusCode,
		Code:        envelope.Error,
		Description: envelope.ErrorDescription,
	}
}

// withOrganizationContext runs call with a valid org-context token for
// orgName, retrying once with a fresh exchange when the server rejects a
// cached token with 401.
func (c *APIClient) withOrganizationContext(
	ctx context.Context, orgName string, call func(token string) (*http.Response, error),
) (*http.Response, error) {
	token, err := c.organizationContextToken(ctx, orgName, false)
	if err != nil {
		return nil, err
	}
	resp, err := call(token)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	token, err = c.organizationContextToken(ctx, orgName, true)
	if err != nil {
		return nil, err
	}
	return call(token)
}

// GetOrganizationContext reads the organization named orgName through the
// org-context route, authenticating with a short-lived exchanged credential.
// The status is 0 for exchange failures (an invalid_target error means the
// organization no longer exists) and the HTTP status otherwise.
func (c *APIClient) GetOrganizationContext(ctx context.Context, orgName string) (*OrganizationRead, int, error) {
	var out OrganizationRead
	resp, err := c.withOrganizationContext(ctx, orgName, func(token string) (*http.Response, error) {
		return c.doJSONAuthorized(ctx, token, http.MethodGet, organizationContextPath, nil, &out, http.StatusOK)
	})
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return &out, http.StatusOK, nil
}

// UpdateOrganizationContext updates the organization named orgName through the
// org-context route. The audience (orgName) must be the organization's
// current name: a rename is delivered in the payload, not in the audience.
func (c *APIClient) UpdateOrganizationContext(ctx context.Context, orgName string, in OrganizationUpdate) (*OrganizationRead, error) {
	var out OrganizationRead
	_, err := c.withOrganizationContext(ctx, orgName, func(token string) (*http.Response, error) {
		return c.doJSONAuthorized(ctx, token, http.MethodPut, organizationContextPath, in, &out, http.StatusOK)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteOrganizationContext deletes the organization named orgName through
// the org-context route.
func (c *APIClient) DeleteOrganizationContext(ctx context.Context, orgName string) error {
	_, err := c.withOrganizationContext(ctx, orgName, func(token string) (*http.Response, error) {
		return c.doJSONAuthorized(ctx, token, http.MethodDelete, organizationContextPath, nil, nil, http.StatusNoContent)
	})
	return err
}

// doFormJSON posts a form-encoded body and decodes a JSON response, sharing
// the transport chain (rate limiting, retries) with doJSON. No Authorization
// header is set: the token endpoint authenticates through the request body.
func (c *APIClient) doFormJSON(ctx context.Context, path string, form url.Values, out any, expectedStatus ...int) (*http.Response, error) {
	reference, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("parse request path: %w", err)
	}
	u := c.BaseURL.ResolveReference(reference)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	for k, vals := range c.headers {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	if !isStatusExpected(resp.StatusCode, expectedStatus) {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		msg := string(b)
		if len(b) == maxErrorBodySize {
			msg += "... (truncated)"
		}
		return resp, &APIError{
			StatusCode: resp.StatusCode,
			Message:    msg,
		}
	}

	if out != nil {
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(out); err != nil && err != io.EOF {
			return resp, fmt.Errorf("decode response: %w", err)
		}
	}

	return resp, nil
}
