// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestAlertModelToCreate_DescriptionAndActive(t *testing.T) {
	t.Parallel()

	channelSet, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"channel-1"})
	if diags.HasError() {
		t.Fatalf("failed to build channel set: %v", diags)
	}

	base := AlertModel{
		Name:       types.StringValue("name"),
		Query:      types.StringValue("select 1"),
		TimeWindow: types.StringValue("5m"),
		Frequency:  types.StringValue("5m"),
		ChannelIDs: channelSet,
		NotifyWhen: types.StringValue("has_matches"),
	}

	t.Run("omits optional fields when null", func(t *testing.T) {
		t.Parallel()

		m := base
		m.Description = types.StringNull()
		m.Active = types.BoolNull()

		create, gotDiags := alertModelToCreate(context.Background(), &m)
		if gotDiags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", gotDiags)
		}
		if create.Description != nil {
			t.Fatalf("expected nil description, got %q", *create.Description)
		}
		if create.Active != nil {
			t.Fatalf("expected nil active, got %v", *create.Active)
		}
	})

	t.Run("preserves empty description and false active", func(t *testing.T) {
		t.Parallel()

		m := base
		m.Description = types.StringValue("")
		m.Active = types.BoolValue(false)

		create, gotDiags := alertModelToCreate(context.Background(), &m)
		if gotDiags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", gotDiags)
		}
		if create.Description == nil || *create.Description != "" {
			t.Fatalf("expected empty description pointer, got %#v", create.Description)
		}
		if create.Active == nil || *create.Active {
			t.Fatalf("expected active=false pointer, got %#v", create.Active)
		}
	})
}

func TestAlertReadToModel_DescriptionAndActive(t *testing.T) {
	t.Parallel()

	t.Run("keeps empty description string", func(t *testing.T) {
		t.Parallel()

		empty := ""
		read := &logclient.AlertRead{
			ID:          "id-1",
			ProjectID:   "proj-1",
			Name:        "name",
			Description: &empty,
			Query:       "select 1",
			TimeWindow:  "PT5M",
			Frequency:   "PT5M",
			Watermark:   "PT10S",
			Channels:    []logclient.ChannelRead{},
			NotifyWhen:  "has_matches",
			Active:      false,
		}

		var got AlertModel
		diags := alertReadToModel(context.Background(), read, &got)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if got.Description.IsNull() || got.Description.ValueString() != "" {
			t.Fatalf("expected empty string description, got %#v", got.Description)
		}
		if got.Active.IsNull() || got.Active.ValueBool() {
			t.Fatalf("expected active=false, got %#v", got.Active)
		}
	})
}
