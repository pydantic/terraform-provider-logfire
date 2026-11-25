// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestAlertModelToCreate_DescriptionAndActive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		description types.String
		active      types.Bool
		wantActive  *bool
	}{
		{
			name:        "uses empty description string when null",
			description: types.StringNull(),
			active:      types.BoolNull(),
			wantActive:  nil,
		},
		{
			name:        "preserves empty description and false active",
			description: types.StringValue(""),
			active:      types.BoolValue(false),
			wantActive:  new(false),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := environmentsBaseModel(t)
			m.Description = tc.description
			m.Active = tc.active

			create, gotDiags := alertModelToCreate(context.Background(), &m)
			if gotDiags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", gotDiags)
			}
			if create.Description == nil || *create.Description != "" {
				t.Fatalf("expected empty description pointer, got %#v", create.Description)
			}
			if !reflect.DeepEqual(create.Active, tc.wantActive) {
				t.Fatalf("active = %v, want %v", create.Active, tc.wantActive)
			}
		})
	}
}

func TestAlertReadToModel_DescriptionAndActive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		prior types.String
	}{
		{name: "keeps empty description string", prior: types.StringValue("")},
		{name: "keeps omitted empty description null", prior: types.StringNull()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			read := testAlertRead()
			read.Description = new("")
			read.Active = false
			got := AlertModel{Description: tc.prior}
			if diags := alertReadToModel(context.Background(), read, &got); diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if !got.Description.Equal(tc.prior) {
				t.Fatalf("description = %#v, want %#v", got.Description, tc.prior)
			}
			if got.Active.IsNull() || got.Active.ValueBool() {
				t.Fatalf("expected active=false, got %#v", got.Active)
			}
		})
	}
}
