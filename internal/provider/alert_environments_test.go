// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func environmentsBaseModel(t *testing.T) AlertModel {
	t.Helper()

	channelSet, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"channel-1"})
	if diags.HasError() {
		t.Fatalf("failed to build channel set: %v", diags)
	}

	return AlertModel{
		Name:       types.StringValue("name"),
		Query:      types.StringValue("select 1"),
		TimeWindow: types.StringValue("5m"),
		Frequency:  types.StringValue("5m"),
		ChannelIDs: channelSet,
		NotifyWhen: types.StringValue("has_matches"),
	}
}

func TestAlertModelToCreate_Environments(t *testing.T) {
	t.Parallel()

	t.Run("omits environments when null", func(t *testing.T) {
		t.Parallel()

		m := environmentsBaseModel(t)
		m.Environments = types.SetNull(types.StringType)

		create, diags := alertModelToCreate(context.Background(), &m)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if len(create.Environments) != 0 {
			t.Fatalf("expected no environments, got %#v", create.Environments)
		}
	})

	t.Run("passes configured environments", func(t *testing.T) {
		t.Parallel()

		envSet, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"production", "staging"})
		if diags.HasError() {
			t.Fatalf("failed to build environments set: %v", diags)
		}
		m := environmentsBaseModel(t)
		m.Environments = envSet

		create, gotDiags := alertModelToCreate(context.Background(), &m)
		if gotDiags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", gotDiags)
		}
		if len(create.Environments) != 2 {
			t.Fatalf("expected 2 environments, got %#v", create.Environments)
		}
		got := map[string]bool{}
		for _, e := range create.Environments {
			got[e] = true
		}
		if !got["production"] || !got["staging"] {
			t.Fatalf("expected production and staging, got %#v", create.Environments)
		}
	})
}

func TestAlertReadToModel_Environments(t *testing.T) {
	t.Parallel()

	read := func(envs []string) *logclient.AlertRead {
		return &logclient.AlertRead{
			ID:           "id-1",
			ProjectID:    "proj-1",
			Name:         "name",
			Query:        "select 1",
			TimeWindow:   "PT5M",
			Frequency:    "PT5M",
			Watermark:    "PT10S",
			Environments: envs,
			Channels:     []logclient.ChannelRead{},
			NotifyWhen:   "has_matches",
			Active:       true,
		}
	}

	t.Run("keeps null when API returns empty and config omitted it", func(t *testing.T) {
		t.Parallel()

		var got AlertModel
		got.Environments = types.SetNull(types.StringType)
		if diags := alertReadToModel(context.Background(), read([]string{}), &got); diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if !got.Environments.IsNull() {
			t.Fatalf("expected null environments, got %#v", got.Environments)
		}
	})

	t.Run("keeps empty set when configured empty", func(t *testing.T) {
		t.Parallel()

		emptySet, diags := types.SetValueFrom(context.Background(), types.StringType, []string{})
		if diags.HasError() {
			t.Fatalf("failed to build empty set: %v", diags)
		}
		var got AlertModel
		got.Environments = emptySet
		if gotDiags := alertReadToModel(context.Background(), read([]string{}), &got); gotDiags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", gotDiags)
		}
		if got.Environments.IsNull() || len(got.Environments.Elements()) != 0 {
			t.Fatalf("expected empty environments set, got %#v", got.Environments)
		}
	})

	t.Run("maps returned environments", func(t *testing.T) {
		t.Parallel()

		var got AlertModel
		got.Environments = types.SetNull(types.StringType)
		if diags := alertReadToModel(context.Background(), read([]string{"production", "staging"}), &got); diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if got.Environments.IsNull() || len(got.Environments.Elements()) != 2 {
			t.Fatalf("expected 2 environments, got %#v", got.Environments)
		}
	})
}
