// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestProjectMissing(t *testing.T) {
	t.Parallel()
	const id = "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed44"
	listed := `[{"id":"` + id + `","project_name":"demo"}]`
	for name, tc := range map[string]struct {
		getStatus  int
		listStatus int
		list       string
		want       bool
	}{
		"readable":             {getStatus: http.StatusOK, want: false},
		"not found":            {getStatus: http.StatusNotFound, want: true},
		"refused and unlisted": {getStatus: http.StatusForbidden, listStatus: http.StatusOK, list: `[]`, want: true},
		"refused but listed":   {getStatus: http.StatusForbidden, listStatus: http.StatusOK, list: listed, want: false},
		"refused, list fails":  {getStatus: http.StatusForbidden, listStatus: http.StatusForbidden, list: `{}`, want: false},
		"server error":         {getStatus: http.StatusInternalServerError, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/oauth/token":
					_, _ = w.Write([]byte(`{"access_token":"test-org-token","expires_in":900}`))
				case "/api/v1/projects/" + id + "/":
					w.WriteHeader(tc.getStatus)
					_, _ = w.Write([]byte(`{"id":"` + id + `","project_name":"demo"}`))
				case "/api/v1/projects/":
					w.WriteHeader(tc.listStatus)
					_, _ = w.Write([]byte(tc.list))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			c, err := logclient.NewAPIClient(server.URL, "test", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if got := projectMissing(context.Background(), c, id); got != tc.want {
				t.Errorf("projectMissing = %v, want %v", got, tc.want)
			}
		})
	}
}
