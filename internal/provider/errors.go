// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"errors"
	"net/http"
)

func isRateLimitError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}
