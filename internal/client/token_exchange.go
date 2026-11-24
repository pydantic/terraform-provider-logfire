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
	"strconv"
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
	organizationContextScopes = "organization:read organization:write organization:create_project organization:read_member organization:write_member organization:read_channel organization:write_channel organization:create_api_key organization:read_api_key organization:write_api_key project:read project:write project:read_token project:write_token project:read_dashboard project:write_dashboard project:read_alert project:write_alert"

	// exchangeExpiresInSeconds mirrors the backend automation example. The
	// server rejects a value above its configured JWT max age.
	exchangeExpiresInSeconds = 900

	// tokenExchangeMinimumRelease is the first release with the
	// instance-admin token exchange.
	tokenExchangeMinimumRelease = "v2026-06-25.01"

	// flightTimeout bounds an exchange that concurrent requests share. It
	// covers a retry without expires_in and the rate limiter's wait.
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
		return fmt.Sprintf(
			"token exchange failed (%s): %s", e.Code, e.Description)
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
	return fmt.Sprintf("cannot act in organization %q: the api_key must be an "+
		"instance-admin API key (scope instance:admin) created in the "+
		"self-hosted admin organization. %s", e.Organization, e.Err)
}

func (e *InstanceAdminRequiredError) Unwrap() error { return e.Err }

// UnknownOrganizationError reports that the token exchange knows no
// organization of this name. The organization never existed, was deleted, or
// was renamed outside Terraform.
type UnknownOrganizationError struct {
	Organization string
	Err          *OAuthTokenError
}

func (e *UnknownOrganizationError) Error() string {
	return fmt.Sprintf("organization %q does not exist on this instance; if "+
		"it was renamed, set the new name", e.Organization)
}

func (e *UnknownOrganizationError) Unwrap() error { return e.Err }

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
// Concurrent requests for one organization share one exchange. The exchange
// runs on a context that cancellation of the first request does not end, and
// without a create's "no automatic retries" marker, because every waiting
// request depends on it. The cache lock is never held across the exchange.
func (c *APIClient) organizationContextToken(ctx context.Context, orgName, rejected string) (string, error) {
	cached := func() (string, bool) {
		c.shared.tokenMu.Lock()
		defer c.shared.tokenMu.Unlock()
		entry, ok := c.shared.tokens[orgName]
		return entry.token, ok && time.Now().Before(entry.expires) &&
			entry.token != rejected
	}
	if token, ok := cached(); ok {
		return token, nil
	}
	result := c.shared.exchanges.DoChan(orgName, func() (any, error) {
		if token, ok := cached(); ok {
			return token, nil
		}
		detached := context.WithoutCancel(ctx)
		detached = context.WithValue(
			detached, disableAutomaticRetriesContextKey{}, false)
		detached, cancel := context.WithTimeout(detached, flightTimeout)
		defer cancel()

		form := url.Values{
			"grant_type": {
				"urn:ietf:params:oauth:grant-type:token-exchange",
			},
			"subject_token": {c.token},
			"subject_token_type": {
				"urn:ietf:params:oauth:token-type:access_token",
			},
			"audience": {
				strings.TrimSuffix(c.BaseURL.String(), "/") + "/" + orgName,
			},
			"scope":      {organizationContextScopes},
			"expires_in": {strconv.Itoa(exchangeExpiresInSeconds)},
		}
		var out tokenExchangeResponse
		err := c.postTokenExchange(detached, form, &out)
		var oauthErr *OAuthTokenError
		if errors.As(err, &oauthErr) && oauthErr.Code == "invalid_request" &&
			strings.HasPrefix(oauthErr.Description, "expires_in") {
			// The instance's JWT max age is shorter. Without expires_in the
			// token lives for that max age.
			form.Del("expires_in")
			err = c.postTokenExchange(detached, form, &out)
		}
		if err != nil {
			// Wrap the two refusals that users can act on. The exchange
			// answers invalid_scope with "subject_token must include
			// instance:admin" to a key that is not an instance-admin key. It
			// answers invalid_target "unknown organization" to an organization
			// name it does not know. The server names the organization it read
			// from the audience, and an extra path in base_url makes that
			// another name, which keeps the server's text. IsInvalidTargetError
			// still holds for the wrapped error.
			if errors.As(err, &oauthErr) {
				unknown := "unknown organization: '" + orgName + "'"
				switch {
				case oauthErr.Code == "invalid_scope" &&
					strings.Contains(oauthErr.Description, "instance:admin"):
					err = &InstanceAdminRequiredError{
						Organization: orgName, Err: oauthErr}
				case oauthErr.Code == "invalid_target" &&
					oauthErr.Description == unknown:
					err = &UnknownOrganizationError{
						Organization: orgName, Err: oauthErr}
				}
			}
			return "", err
		}
		if out.AccessToken == "" {
			return "", errors.New("token exchange returned no access token")
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
		c.shared.tokens[orgName] = orgTokenEntry{
			token:   out.AccessToken,
			expires: time.Now().Add(valid),
		}
		c.shared.tokenMu.Unlock()
		return out.AccessToken, nil
	})
	select {
	case r := <-result:
		token, _ := r.Val.(string)
		return token, r.Err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// postTokenExchange posts a form-encoded body and decodes a JSON response,
// sharing the transport chain (rate limiting, retries) with doJSON. No
// Authorization header is set: the token endpoint authenticates through the
// request body.
//
// A failed answer with the RFC 6749 flat envelope becomes an OAuthTokenError. A
// 404 without it means the instance does not serve the exchange. Any other
// answer keeps its message but drops its HTTP status, because callers read a
// 404 from a resource route as a deleted resource, and an exchange failure
// must never look like one.
func (c *APIClient) postTokenExchange(ctx context.Context, form url.Values, out *tokenExchangeResponse) error {
	body := strings.NewReader(form.Encode())
	req, err := c.newRequest(ctx, http.MethodPost, tokenExchangePath, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.send(req, http.StatusOK)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		var envelope struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		jsonErr := json.Unmarshal([]byte(apiErr.Message), &envelope)
		switch {
		case jsonErr == nil && envelope.Error != "":
			return &OAuthTokenError{
				StatusCode:  apiErr.StatusCode,
				Code:        envelope.Error,
				Description: envelope.ErrorDescription,
			}
		case apiErr.StatusCode == http.StatusNotFound:
			return &EndpointUnavailableError{
				Method:         http.MethodPost,
				Path:           tokenExchangePath,
				MinimumRelease: tokenExchangeMinimumRelease,
				BackendVersion: apiErr.BackendVersion,
			}
		}
		return fmt.Errorf("token exchange failed: %s", apiErr.Error())
	}
	if err != nil {
		return err
	}
	defer drainResponse(resp)

	err = json.NewDecoder(resp.Body).Decode(out)
	if err != nil && err != io.EOF {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// withOrganizationContext runs the call with a valid token for orgName,
// retrying once with a fresh exchange when the server rejects the token with
// 401.
func (c *APIClient) withOrganizationContext(ctx context.Context, orgName, method, path string, in, out any, expectedStatus ...int) (*http.Response, error) {
	token, err := c.organizationContextToken(ctx, orgName, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.doJSONAuthorized(
		ctx, token, method, path, in, out, expectedStatus...)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	token, err = c.organizationContextToken(ctx, orgName, token)
	if err != nil {
		return nil, err
	}
	return c.doJSONAuthorized(
		ctx, token, method, path, in, out, expectedStatus...)
}

// GetOrganizationContext reads the organization named orgName through the
// org-context route, authenticating with a short-lived exchanged credential.
// The status is 0 for exchange failures (an invalid_target error means the
// organization no longer exists) and the HTTP status otherwise.
func (c *APIClient) GetOrganizationContext(ctx context.Context, orgName string) (*OrganizationRead, int, error) {
	var out OrganizationRead
	resp, err := c.withOrganizationContext(ctx, orgName, http.MethodGet,
		organizationContextPath, nil, &out, http.StatusOK)
	return withStatus(&out, resp, err)
}

// UpdateOrganizationContext updates the organization named orgName through the
// org-context route. The audience (orgName) must be the organization's
// current name: a rename is delivered in the payload, not in the audience.
func (c *APIClient) UpdateOrganizationContext(ctx context.Context, orgName string, in OrganizationUpdate) (*OrganizationRead, error) {
	var out OrganizationRead
	_, err := c.withOrganizationContext(ctx, orgName, http.MethodPut,
		organizationContextPath, in, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	if out.OrganizationName != orgName {
		c.forgetOrganizationToken(orgName)
	}
	return &out, nil
}

// forgetOrganizationToken drops the cached token for a name that a rename or a
// delete freed. The token stays bound to the old organization, so another
// organization that takes the name must get its own.
func (c *APIClient) forgetOrganizationToken(orgName string) {
	c.shared.tokenMu.Lock()
	defer c.shared.tokenMu.Unlock()
	delete(c.shared.tokens, orgName)
}

// DeleteOrganizationContext deletes the organization named orgName through
// the org-context route.
func (c *APIClient) DeleteOrganizationContext(ctx context.Context, orgName string) error {
	_, err := c.withOrganizationContext(ctx, orgName, http.MethodDelete,
		organizationContextPath, nil, nil, http.StatusNoContent)
	if err == nil {
		c.forgetOrganizationToken(orgName)
	}
	return err
}
