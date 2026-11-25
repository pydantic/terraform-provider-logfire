// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"net/http"
	"strings"
	"testing"
)

// An answer with no body decodes to nothing, so a list would look empty and a refresh would drop every
// resource in it. A response that the caller expects to read must therefore have a body.
func TestListProjectsRejectsAnEmptyBody(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"empty": "", "blank": "  \n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newStubClient(t, func(req *http.Request) (*http.Response, error) {
				return stubResponse(req, http.StatusOK, body), nil
			})
			projects, err := c.ListProjects(t.Context())
			if err == nil || !strings.Contains(err.Error(), "empty body") {
				t.Fatalf("ListProjects = %v, %v, want an error that names the empty body", projects, err)
			}
		})
	}
}

// An answer that carries no content on purpose is not an empty body.
func TestNoContentAnswerNeedsNoBody(t *testing.T) {
	t.Parallel()
	c := newStubClient(t, func(req *http.Request) (*http.Response, error) {
		return stubResponse(req, http.StatusNoContent, ""), nil
	})
	var out struct{}
	if _, err := c.doJSON(t.Context(), http.MethodPatch, "/x/", nil, &out, http.StatusNoContent); err != nil {
		t.Fatalf("a 204 with an output to fill must succeed: %v", err)
	}
}
