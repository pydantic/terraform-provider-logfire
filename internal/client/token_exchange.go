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

	// organizationResourceScopes are the scopes of the token behind the
	// organization read, update, and delete routes: `organization:read` for
	// the read route, `organization:write` for update and delete.
	organizationResourceScopes = "organization:read organization:write"

	// organizationContextScopes are the scopes of the token that a client from
	// ForOrganization sends. They are the union of the scopes that the
	// organization-scoped resources document, minus the scopes the exchange
	// refuses (instance:*, organization:admin, and project-bound scopes). The
	// token is a service caller without a user, so this set is its whole
	// permission boundary. The gateway scopes are absent on purpose: instances
	// before 2026-10-15 reject an unknown scope for the whole exchange, and
	// organization:read and organization:write cover the gateway routes.
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
	// Audience is the audience that an invalid_target answer rejected.
	Audience string
}

func (e *OAuthTokenError) Error() string {
	message := fmt.Sprintf("token exchange failed (%s)", e.Code)
	if e.Description != "" {
		message += ": " + e.Description
	}
	if e.Audience != "" {
		message += fmt.Sprintf(". The audience was %q: the provider base_url followed by the organization name", e.Audience)
	}
	return message
}

// IsInvalidTargetError reports whether the error is a token-exchange rejection
// whose audience names no existing organization. For a managed organization
// that means it no longer exists. The code is ambiguous: an audience that does
// not match the instance's base URL gets it too.
func IsInvalidTargetError(err error) bool {
	var oauthErr *OAuthTokenError
	return errors.As(err, &oauthErr) && oauthErr.Code == "invalid_target"
}

// InstanceAdminRequiredError reports that the token exchange refused the
// credential because it is not an instance-admin key.
type InstanceAdminRequiredError struct {
	Organization string
	Err          *OAuthTokenError
}

func (e *InstanceAdminRequiredError) Error() string {
	return fmt.Sprintf("cannot act in organization %q: the api_key must be an instance-admin API key "+
		"(scope instance:admin or organization:admin) created in the self-hosted admin organization. %s",
		e.Organization, e.Err)
}

func (e *InstanceAdminRequiredError) Unwrap() error { return e.Err }

// IsInstanceAdminRequiredError reports whether the error is a token-exchange
// refusal because the credential lacks instance-admin rights.
func IsInstanceAdminRequiredError(err error) bool {
	var adminErr *InstanceAdminRequiredError
	return errors.As(err, &adminErr)
}

// orgTokenKey identifies a cached token. Two scope sets for one organization
// are two tokens.
type orgTokenKey struct {
	organization string
	scopes       string
}

type orgTokenEntry struct {
	token   string
	expires time.Time
}

type tokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// organizationContextToken returns a short-lived credential for orgName with
// the given scopes, exchanging the configured instance-admin credential via
// the OAuth token endpoint and caching the result until shortly before its
// expiry. rejected is a token the server just refused with 401. A cached token
// that differs from it replaced it already, and is returned without another
// exchange, because the token endpoint allows 20 exchanges a minute from one
// address.
//
// Concurrent callers for one organization and scope set share one exchange,
// because Terraform runs up to ten operations at once. The exchange runs on a
// context that the first caller's cancellation does not end. The cache lock is
// never held across the exchange, so an exchange for one organization never
// blocks another organization.
func (c *APIClient) organizationContextToken(ctx context.Context, orgName, scopes, rejected string) (string, error) {
	key := orgTokenKey{organization: orgName, scopes: scopes}
	c.shared.orgTokenMu.Lock()
	entry, ok := c.shared.orgTokenCache[key]
	c.shared.orgTokenMu.Unlock()
	if ok && time.Now().Before(entry.expires) && entry.token != rejected {
		return entry.token, nil
	}
	token, err := c.shareFlight(ctx, orgName+"\n"+scopes, func(ctx context.Context) (any, error) {
		return c.exchangeToken(ctx, key)
	})
	if err != nil {
		return "", err
	}
	tokenString, _ := token.(string)
	return tokenString, nil
}

// exchangeToken runs one exchange for key and caches the token.
func (c *APIClient) exchangeToken(ctx context.Context, key orgTokenKey) (string, error) {
	form := url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {c.token},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"audience":           {strings.TrimSuffix(c.BaseURL.String(), "/") + "/" + key.organization},
		"scope":              {key.scopes},
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
	if errors.As(err, &oauthErr) && oauthErr.Code == "invalid_target" {
		oauthErr.Audience = form.Get("audience")
	}
	if err != nil {
		return "", asInstanceAdminRequired(key.organization, err)
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
	c.shared.orgTokenMu.Lock()
	c.shared.orgTokenCache[key] = orgTokenEntry{token: out.AccessToken, expires: time.Now().Add(valid)}
	c.shared.orgTokenMu.Unlock()
	return out.AccessToken, nil
}

// postTokenExchange sends one exchange form and decodes the token.
func (c *APIClient) postTokenExchange(ctx context.Context, form url.Values, out *tokenExchangeResponse) error {
	_, err := c.doFormJSON(ctx, tokenExchangePath, form, out, http.StatusOK)
	return tokenEndpointError(err)
}

// tokenEndpointError converts a failed token-endpoint answer. A body with the
// RFC 6749 flat envelope becomes an OAuthTokenError. A 404 without it means
// that the instance does not serve the exchange. Any other answer keeps its
// message but drops its HTTP status, because callers read a 404 from a
// resource route as a deleted resource, and an exchange failure must never
// look like one.
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

// asInstanceAdminRequired wraps the exchange's answer to a credential that is
// not an instance-admin key. The exchange answers invalid_scope with
// "subject_token must include instance:admin".
func asInstanceAdminRequired(orgName string, err error) error {
	var oauthErr *OAuthTokenError
	if errors.As(err, &oauthErr) && oauthErr.Code == "invalid_scope" && strings.Contains(oauthErr.Description, "instance:admin") {
		return &InstanceAdminRequiredError{Organization: orgName, Err: oauthErr}
	}
	return err
}

// withOrganizationContext runs call with a valid token for orgName with the
// given scopes, retrying once with a fresh exchange when the server rejects a
// cached token with 401.
func (c *APIClient) withOrganizationContext(
	ctx context.Context, orgName, scopes string, call func(token string) (*http.Response, error),
) (*http.Response, error) {
	token, err := c.organizationContextToken(ctx, orgName, scopes, "")
	if err != nil {
		return nil, err
	}
	resp, err := call(token)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	token, err = c.organizationContextToken(ctx, orgName, scopes, token)
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
	resp, err := c.withOrganizationContext(ctx, orgName, organizationResourceScopes, func(token string) (*http.Response, error) {
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
	_, err := c.withOrganizationContext(ctx, orgName, organizationResourceScopes, func(token string) (*http.Response, error) {
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
	_, err := c.withOrganizationContext(ctx, orgName, organizationResourceScopes, func(token string) (*http.Response, error) {
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

	resp, err := c.guardedHTTP().Do(req)
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

// guardedHTTP returns the HTTP client with the redirect rule of sameHostRedirect.
// Every request carries the API key or an exchanged token and the custom
// headers, and Go forwards custom headers to any host that a redirect names.
func (c *APIClient) guardedHTTP() *http.Client {
	guarded := *c.HTTP
	guarded.CheckRedirect = sameHostRedirect
	return &guarded
}

// sameHostRedirect follows a redirect only to the original host, and never
// from https to http. A redirect from http to https on the same host is
// allowed, and so is a default port written out. Go sends a form body again
// after a 307 or 308 redirect, and a redirect keeps the custom headers.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	first := via[0].URL
	upgrade := first.Scheme == "http" && req.URL.Scheme == "https"
	samePort := effectivePort(req.URL) == effectivePort(first)
	if !strings.EqualFold(req.URL.Hostname(), first.Hostname()) ||
		(first.Scheme == "https" && req.URL.Scheme != "https") ||
		(!samePort && !upgrade) {
		return fmt.Errorf("refusing the redirect to %s, because the provider sends its API key and headers only to %s. "+
			"Set base_url to the instance's own URL", req.URL.Redacted(), first.Host)
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
