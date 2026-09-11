// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"net/http"
)

// IsRateLimitError reports whether the error represents an HTTP 429.
func IsRateLimitError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// IsNotFoundError reports whether the error represents an HTTP 404.
func IsNotFoundError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// IsConflictError reports whether the error represents an HTTP 409.
func IsConflictError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusConflict
	}
	return false
}

// BackendVersionFromError returns the instance version reported by an API
// error's x-backend-version response header, when the instance provides one.
// Instances old enough to be missing API routes usually predate the header,
// so this is often empty.
func BackendVersionFromError(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.BackendVersion
	}
	return ""
}

// EndpointUnavailableError reports a 404 from an API collection route whose
// only possible 404 meaning is that the route does not exist on the instance:
// the instance predates that API. Resources surface it directly, so the
// message names the minimum release (and Helm chart, when known) and any
// version the instance did report.
type EndpointUnavailableError struct {
	Method         string
	Path           string
	MinimumRelease string
	MinimumChart   string
	BackendVersion string
}

func (e *EndpointUnavailableError) Error() string {
	message := "this Logfire instance does not expose " + e.Method + " " + e.Path +
		", which requires Logfire " + e.MinimumRelease + " or newer"
	if e.MinimumChart != "" {
		message += " (Helm chart " + e.MinimumChart + " or newer)"
	}
	if e.BackendVersion != "" {
		message += "; the instance reports version " + e.BackendVersion
	}
	return message + ". Upgrade the instance, or manage this resource in the Logfire UI until then."
}
