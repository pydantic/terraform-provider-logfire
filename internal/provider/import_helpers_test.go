// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestSplitImportParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		raw           string
		allowedCounts []int
		want          []string
		wantError     bool
	}{
		{name: "slash", raw: " project-id / application-id ", allowedCounts: []int{2}, want: []string{"project-id", "application-id"}},
		{name: "comma", raw: "project-id,application-id", allowedCounts: []int{2}, want: []string{"project-id", "application-id"}},
		{name: "pipe", raw: "project-id|application-id|token-id", allowedCounts: []int{3}, want: []string{"project-id", "application-id", "token-id"}},
		{name: "one of several counts", raw: "project-id/application-id", allowedCounts: []int{1, 2}, want: []string{"project-id", "application-id"}},
		{name: "empty", raw: "", allowedCounts: []int{2}, wantError: true},
		{name: "leading separator", raw: "/project-id/application-id", allowedCounts: []int{2}, wantError: true},
		{name: "trailing separator", raw: "project-id/application-id/", allowedCounts: []int{2}, wantError: true},
		{name: "repeated separator", raw: "project-id//application-id", allowedCounts: []int{2}, wantError: true},
		{name: "mixed repeated separators", raw: "project-id/,application-id", allowedCounts: []int{2}, wantError: true},
		{name: "whitespace segment", raw: "project-id/ /application-id", allowedCounts: []int{3}, wantError: true},
		{name: "wrong count", raw: "project-id/application-id/token-id", allowedCounts: []int{2}, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := splitImportParts(tt.raw, tt.allowedCounts...)
			if tt.wantError {
				if err == nil {
					t.Fatalf("splitImportParts(%q) returned %v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitImportParts(%q) returned error: %v", tt.raw, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("splitImportParts(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestFindProjectByNameOrIDPrefersIDMatch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"11111111-1111-1111-1111-111111111111","project_name":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"},
			{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","project_name":"production"}
		]`))
	}))
	defer server.Close()
	c, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	id, name, err := findProjectByNameOrID(t.Context(), c, "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff")
	if err != nil {
		t.Fatal(err)
	}
	if id != "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff" || name != "production" {
		t.Fatalf("resolved id=%q name=%q, want the ID match", id, name)
	}
	nameID, name, err := findProjectByNameOrID(t.Context(), c, "production")
	if err != nil {
		t.Fatal(err)
	}
	if nameID != "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff" || name != "production" {
		t.Fatalf("resolved project name to id=%q name=%q", nameID, name)
	}
}
