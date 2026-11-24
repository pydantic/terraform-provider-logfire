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

	// organizationContextScopes are the scopes of every token that a client
	// from ForOrganization sends, the organization routes included. One set
	// means one exchange per organization, and the token endpoint allows 20
	// exchanges a minute and 100 an hour from one address. The token is a
	// service caller without a user, so this set is its whole permission
	// boundary. The exchange refuses instance:*, organization:admin, and
	// project-bound scopes. The gateway scopes are absent on purpose:
	// instances before 2026-10-15 reject an unknown scope for the whole
	// exchange, and organization:read and organization:write cover the
	// gateway routes.
	organizationContextScopes = "organization:read organization:write organization:create_project " +
		"organization:read_member organization:write_member " +
		"organization:read_channel organization:write_channel " +
		"organization:create_api_key organization:read_api_key organization:write_api_key " +
		"project:read project:write project:read_token project:write_token " +
		"project:read_dashboard project:write_dashboard project:read_alert project:write_alert"

	// exchangeExpiresInSeconds mirrors the backend automation example. The
	// server rejects a value above its configured JWT max age.
	exchangeExpiresInSeconds = 900

	// tokenExchangeMinimumRelease is the first release with the
	// instance-admin token exchange.
	tokenExchangeMinimumRelease = "v2026-06-25.01"

	// flightTimeout bounds an exchange that concurrent requests share. It covers
	// a retry without expires_in and the rate limiter's wait.
	flightTimeout = 3 * defaultRequestTimeout

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

// InstanceAdminRequiredError reports that the token exchange refused the API
// key because it is not an instance-admin key.
type InstanceAdminRequiredError struct {
	Organization string
	Err          *OAuthTokenError
}

func (e *InstanceAdminRequiredError) Error() string {
	return fmt.Sprintf("cannot act in organization %q: the api_key must be an instance-admin API key "+
		"(scope instance:admin) created in the self-hosted admin organization. %s", e.Organization, e.Err)
}

func (e *InstanceAdminRequiredError) Unwrap() error { return e.Err }

type orgTokenEntry struct {
	token   string
	expires time.Time
}

type tokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// organizationContextToken returns a short-lived token for orgName, exchanging
// the instance-admin API key at the OAuth token endpoint and caching the result
// until shortly before its expiry. rejected is a token that the server just
// refused with 401. A cached token that differs from it replaced it already and
// is returned without another exchange.
//
// Concurrent requests for one organization share one exchange. The exchange runs
// on a context that cancellation of the first request does not end, and without a
// create's "no automatic retries" marker, because every waiting request depends
// on it. The cache lock is never held across the exchange.
func (c *APIClient) organizationContextToken(ctx context.Context, orgName, rejected string) (string, error) {
	cached := func() (string, bool) {
		c.shared.tokenMu.Lock()
		defer c.shared.tokenMu.Unlock()
		entry, ok := c.shared.tokens[orgName]
		return entry.token, ok && time.Now().Before(entry.expires) && entry.token != rejected
	}
	if token, ok := cached(); ok {
		return token, nil
	}
	result := c.shared.exchanges.DoChan(orgName, func() (any, error) {
		if token, ok := cached(); ok {
			return token, nil
		}
		detached, cancel := context.WithTimeout(enableAutomaticRetries(context.WithoutCancel(ctx)), flightTimeout)
		defer cancel()
		return c.exchangeToken(detached, orgName)
	})
	select {
	case r := <-result:
		token, _ := r.Val.(string)
		return token, r.Err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// exchangeToken runs one exchange for orgName and caches the token.
func (c *APIClient) exchangeToken(ctx context.Context, orgName string) (string, error) {
	form := url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {c.token},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"audience":           {strings.TrimSuffix(c.BaseURL.String(), "/") + "/" + orgName},
		"scope":              {organizationContextScopes},
		"expires_in":         {fmt.Sprintf("%d", exchangeExpiresInSeconds)},
	}
	var out tokenExchangeResponse
	err := c.postTokenExchange(ctx, form, &out)
	var oauthErr *OAuthTokenError
	if errors.As(err, &oauthErr) && oauthErr.Code == "invalid_request" && strings.HasPrefix(oauthErr.Description, "expires_in") {
		// The instance's JWT max age is shorter. Without expires_in the token
		// lives for that max age.
		form.Del("expires_in")
		err = c.postTokenExchange(ctx, form, &out)
	}
	if err != nil {
		return "", asInstanceAdminRequired(orgName, err)
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
	c.shared.tokenMu.Lock()
	c.shared.tokens[orgName] = orgTokenEntry{token: out.AccessToken, expires: time.Now().Add(valid)}
	c.shared.tokenMu.Unlock()
	return out.AccessToken, nil
}

func (c *APIClient) postTokenExchange(ctx context.Context, form url.Values, out *tokenExchangeResponse) error {
	_, err := c.doFormJSON(ctx, tokenExchangePath, form, out, http.StatusOK)
	return tokenEndpointError(err)
}

// tokenEndpointError converts a failed token-endpoint answer. A body with the
// RFC 6749 flat envelope becomes an OAuthTokenError. A 404 without it means the
// instance does not serve the exchange. Any other answer keeps its message but
// drops its HTTP status, because callers read a 404 from a resource route as a
// deleted resource, and an exchange failure must never look like one.
func tokenEndpointError(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	var envelope struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if jsonErr := json.Unmarshal([]byte(apiErr.Message), &envelope); jsonErr == nil && envelope.Error != "" {
		return &OAuthTokenError{
			StatusCode:  apiErr.StatusCode,
			Code:        envelope.Error,
			Description: envelope.ErrorDescription,
		}
	}
	if apiErr.StatusCode == http.StatusNotFound {
		return &EndpointUnavailableError{
			Method:         http.MethodPost,
			Path:           tokenExchangePath,
			MinimumRelease: tokenExchangeMinimumRelease,
			BackendVersion: apiErr.BackendVersion,
		}
	}
	return fmt.Errorf("token exchange failed: %s", apiErr.Error())
}

// asInstanceAdminRequired wraps the answer to a key that is not an instance-admin
// key. The exchange answers invalid_scope with "subject_token must include
// instance:admin".
func asInstanceAdminRequired(orgName string, err error) error {
	var oauthErr *OAuthTokenError
	if errors.As(err, &oauthErr) && oauthErr.Code == "invalid_scope" && strings.Contains(oauthErr.Description, "instance:admin") {
		return &InstanceAdminRequiredError{Organization: orgName, Err: oauthErr}
	}
	return err
}

// withOrganizationContext runs call with a valid token for orgName, retrying
// once with a fresh exchange when the server rejects the token with 401.
func (c *APIClient) withOrganizationContext(
	ctx context.Context, orgName string, call func(token string) (*http.Response, error),
) (*http.Response, error) {
	token, err := c.organizationContextToken(ctx, orgName, "")
	if err != nil {
		return nil, err
	}
	resp, err := call(token)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	token, err = c.organizationContextToken(ctx, orgName, token)
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
		return resp, newAPIError(resp, msg)
	}

	if out != nil {
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(out); err != nil && err != io.EOF {
			return resp, fmt.Errorf("decode response: %w", err)
		}
	}

	return resp, nil
}
