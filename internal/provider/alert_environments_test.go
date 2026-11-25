// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func environmentsBaseModel(t *testing.T) AlertModel {
	t.Helper()

	return AlertModel{
		Name:               types.StringValue("name"),
		Query:              types.StringValue("select 1"),
		TimeWindow:         newAlertDurationValue("5m"),
		Frequency:          newAlertDurationValue("5m"),
		ChannelAssignments: testChannelAssignments(t, logclient.ChannelAssignment{ChannelID: "channel-1"}),
		NotifyWhen:         types.StringValue("has_matches"),
	}
}

func testAlertRead() *logclient.AlertRead {
	return &logclient.AlertRead{
		ID:         "id-1",
		ProjectID:  "proj-1",
		Name:       "name",
		Query:      "select 1",
		TimeWindow: "PT5M",
		Frequency:  "PT5M",
		Watermark:  "PT10S",
		Channels:   []logclient.AlertChannelRead{},
		NotifyWhen: "has_matches",
		Active:     true,
	}
}

func TestAlertModelToCreate_Environments(t *testing.T) {
	t.Parallel()

	envSet, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"production", "staging"})
	if diags.HasError() {
		t.Fatalf("failed to build environments set: %v", diags)
	}

	cases := []struct {
		name         string
		environments types.Set
		want         []string
	}{
		{name: "omits environments when null", environments: types.SetNull(types.StringType), want: nil},
		{name: "passes configured environments", environments: envSet, want: []string{"production", "staging"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := environmentsBaseModel(t)
			m.Environments = tc.environments

			create, diags := alertModelToCreate(context.Background(), &m)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if got := slices.Sorted(slices.Values(create.Environments)); !slices.Equal(got, tc.want) {
				t.Fatalf("environments = %#v, want %#v", create.Environments, tc.want)
			}
		})
	}
}

func TestAlertReadToModel_Environments(t *testing.T) {
	t.Parallel()

	emptySet, diags := types.SetValueFrom(context.Background(), types.StringType, []string{})
	if diags.HasError() {
		t.Fatalf("failed to build empty set: %v", diags)
	}

	cases := []struct {
		name     string
		prior    types.Set
		returned []string
		wantNull bool
		wantLen  int
	}{
		{
			name:     "keeps null when API returns empty and config omitted it",
			prior:    types.SetNull(types.StringType),
			returned: []string{},
			wantNull: true,
		},
		{
			name:     "keeps empty set when configured empty",
			prior:    emptySet,
			returned: []string{},
		},
		{
			name:     "maps returned environments",
			prior:    types.SetNull(types.StringType),
			returned: []string{"production", "staging"},
			wantLen:  2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			read := testAlertRead()
			read.Environments = tc.returned
			got := AlertModel{Environments: tc.prior}
			if diags := alertReadToModel(context.Background(), read, &got); diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if got.Environments.IsNull() != tc.wantNull || len(got.Environments.Elements()) != tc.wantLen {
				t.Fatalf("environments = %#v, want null=%v with %d elements", got.Environments, tc.wantNull, tc.wantLen)
			}
		})
	}
}
