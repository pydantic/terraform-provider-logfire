// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestParseDurationStr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  string
		want   time.Duration
		hasErr bool
	}{
		{name: "minutes", input: "5m", want: 5 * time.Minute},
		{name: "hours", input: "12h", want: 12 * time.Hour},
		{name: "day shorthand", input: "30d", want: 30 * 24 * time.Hour},
		{name: "iso day", input: "P30D", want: 30 * 24 * time.Hour},
		{name: "invalid", input: "not-a-duration", hasErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDurationStr(types.StringValue(tc.input))
			if tc.hasErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("parseDurationStr(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestDurationCompact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input time.Duration
		want  string
	}{
		{name: "minutes", input: 5 * time.Minute, want: "5m"},
		{name: "day remains hours", input: 24 * time.Hour, want: "24h"},
		{name: "week as day shorthand", input: 7 * 24 * time.Hour, want: "7d"},
		{name: "month as day shorthand", input: 30 * 24 * time.Hour, want: "30d"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := durationCompact(tc.input); got != tc.want {
				t.Fatalf("durationCompact(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
