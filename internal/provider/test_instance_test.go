// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"net/http/httptest"
)

const ownOrganizationPath = "/api/v1/organization/"

// newTestInstance serves handler as a Logfire instance. The provider reads the
// organization of its key once per process, and the instances of most tests do
// not model that route, so they answer it as an instance without it does.
func newTestInstance(handler http.Handler) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == ownOrganizationPath {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
			return
		}
		handler.ServeHTTP(w, r)
	}))
}
