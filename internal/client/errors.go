// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"encoding/json"
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

// isMissingRoute reports a 404 for a route the instance does not serve, which
// FastAPI answers with the detail "Not Found". A route that looks up an item
// answers a missing item with its own detail.
func isMissingRoute(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return false
	}
	var body struct {
		Detail string `json:"detail"`
	}
	return json.Unmarshal([]byte(apiErr.Message), &body) == nil && body.Detail == "Not Found"
}

// IsConflictError reports whether the error represents an HTTP 409.
func IsConflictError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusConflict
	}
	return false
}

// BackendVersionFromError returns the instance's release version reported by an
// API error's Logfire-Version response header, when the instance provides a
// release tag. Builds with no release tag answer with the image identity
// instead, which is not comparable, so this is empty for them; instances old
// enough to be missing API routes usually predate the header entirely.
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
