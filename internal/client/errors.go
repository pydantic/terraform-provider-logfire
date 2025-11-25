// Copyright (c) Pydantic, Inc.
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
