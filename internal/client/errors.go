// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"net/http"
)

func hasStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

// IsRateLimitError reports whether the error represents an HTTP 429.
func IsRateLimitError(err error) bool {
	return hasStatus(err, http.StatusTooManyRequests)
}

// IsNotFoundError reports whether the error represents an HTTP 404.
func IsNotFoundError(err error) bool {
	return hasStatus(err, http.StatusNotFound)
}

// IsConflictError reports whether the error represents an HTTP 409.
func IsConflictError(err error) bool {
	return hasStatus(err, http.StatusConflict)
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
	message := "this Logfire instance does not expose " + e.Method + " " +
		e.Path + ", which requires Logfire " + e.MinimumRelease + " or newer"
	if e.MinimumChart != "" {
		message += " (Helm chart " + e.MinimumChart + " or newer)"
	}
	if e.BackendVersion != "" {
		message += "; the instance reports version " + e.BackendVersion
	}
	return message + ". Upgrade the instance, or manage this resource in the Logfire UI until then."
}

// unavailableIfNotFound turns a 404 from a collection route into an
// EndpointUnavailableError and returns any other error, or nil, unchanged.
func unavailableIfNotFound(err error, method, path, minimumRelease, minimumChart string) error {
	if !IsNotFoundError(err) {
		return err
	}
	return &EndpointUnavailableError{
		Method:         method,
		Path:           path,
		MinimumRelease: minimumRelease,
		MinimumChart:   minimumChart,
		BackendVersion: BackendVersionFromError(err),
	}
}
