// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"slices"
	"testing"
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
