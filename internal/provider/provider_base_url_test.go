// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"
)

func TestInferBaseURLFromAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		apiKey  string
		want    string
		wantErr string
	}{
		{
			name:   "v1 us",
			apiKey: "pylf_v1_us_abc123",
			want:   defaultUSBaseURL,
		},
		{
			name:   "v1 eu",
			apiKey: "pylf_v1_eu_abc123",
			want:   defaultEUBaseURL,
		},
		{
			name:   "v2 us",
			apiKey: "pylf_v2_us_123e4567-e89b-12d3-a456-426614174000_abc123",
			want:   defaultUSBaseURL,
		},
		{
			name:   "v2 eu",
			apiKey: "pylf_v2_eu_123e4567-e89b-12d3-a456-426614174000_abc123",
			want:   defaultEUBaseURL,
		},
		{
			name:    "unsupported region",
			apiKey:  "pylf_v1_local_abc123",
			wantErr: `unsupported api_key region "local"`,
		},
		{
			name:    "invalid format",
			apiKey:  "not-a-logfire-token",
			wantErr: "invalid api_key format",
		},
		{
			name:    "invalid v2 format",
			apiKey:  "pylf_v2_us_missing-token",
			wantErr: "invalid api_key format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := inferBaseURLFromAPIKey(tt.apiKey)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("inferBaseURLFromAPIKey(%q) succeeded unexpectedly", tt.apiKey)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("inferBaseURLFromAPIKey(%q) error = %q, want substring %q", tt.apiKey, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("inferBaseURLFromAPIKey(%q) error = %v", tt.apiKey, err)
			}
			if got != tt.want {
				t.Fatalf("inferBaseURLFromAPIKey(%q) = %q, want %q", tt.apiKey, got, tt.want)
			}
		})
	}
}

func TestAPIKeyRegionRejectsUnknownVersion(t *testing.T) {
	t.Parallel()

	_, err := apiKeyRegion("pylf_v3_us_abc123")
	if err == nil {
		t.Fatal("apiKeyRegion succeeded unexpectedly")
	}
	if !strings.Contains(err.Error(), `unsupported api_key format "v3"`) {
		t.Fatalf("apiKeyRegion error = %q", err.Error())
	}
}
