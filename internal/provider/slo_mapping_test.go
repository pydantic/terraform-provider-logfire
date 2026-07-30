// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func baseSloModel() SloModel {
	return SloModel{
		ProjectID:     types.StringValue("proj-1"),
		ScopeKind:     types.StringValue("service"),
		ScopeValue:    types.StringValue("payments-api"),
		Name:          types.StringValue("payments 99.9% / 30d"),
		TotalQuery:    types.StringValue("parent_span_id IS NULL"),
		BadQuery:      types.StringValue("otel_status_code = 'ERROR'"),
		TargetPercent: types.StringValue("99.9"),
		RollingWindow: types.StringValue("30d"),
	}
}

func TestSloModelToCreate(t *testing.T) {
	t.Parallel()

	t.Run("maps required fields and converts the window to seconds", func(t *testing.T) {
		t.Parallel()

		m := baseSloModel()
		create, diags := sloModelToCreate(context.Background(), &m)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if create.ScopeKind != "service" || create.ScopeValue != "payments-api" || create.Name != "payments 99.9% / 30d" {
			t.Fatalf("unexpected identity fields: %#v", create)
		}
		if create.TargetPercent != "99.9" {
			t.Fatalf("expected target 99.9, got %q", create.TargetPercent)
		}
		if create.RollingWindowSeconds != 30*86400 {
			t.Fatalf("expected 30d in seconds, got %d", create.RollingWindowSeconds)
		}
		if create.Description != nil || create.Source != nil || create.MetricAggregation != nil || create.Environments != nil {
			t.Fatalf("expected omitted optionals, got %#v", create)
		}
		if create.PageChannelIDs != nil || create.TicketChannelIDs != nil {
			t.Fatalf("expected omitted channel seeds, got %#v", create)
		}
	})

	t.Run("includes optional description, source, metric_aggregation, and environments", func(t *testing.T) {
		t.Parallel()

		envs, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"prod"})
		if diags.HasError() {
			t.Fatalf("failed to build environments set: %v", diags)
		}
		m := baseSloModel()
		m.Description = types.StringValue("desc")
		m.Source = types.StringValue("metrics")
		m.MetricAggregation = types.StringValue("counter_rate")
		m.Environments = envs

		create, gotDiags := sloModelToCreate(context.Background(), &m)
		if gotDiags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", gotDiags)
		}
		if create.Description == nil || *create.Description != "desc" {
			t.Fatalf("unexpected description: %#v", create.Description)
		}
		if create.Source == nil || *create.Source != "metrics" {
			t.Fatalf("unexpected source: %#v", create.Source)
		}
		if create.MetricAggregation == nil || *create.MetricAggregation != "counter_rate" {
			t.Fatalf("unexpected metric_aggregation: %#v", create.MetricAggregation)
		}
		if len(create.Environments) != 1 || create.Environments[0] != "prod" {
			t.Fatalf("unexpected environments: %#v", create.Environments)
		}
	})

	t.Run("includes the burn-rate alert channel seeds", func(t *testing.T) {
		t.Parallel()

		page, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"chan-page"})
		if diags.HasError() {
			t.Fatalf("failed to build page channel set: %v", diags)
		}
		ticket, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"chan-ticket"})
		if diags.HasError() {
			t.Fatalf("failed to build ticket channel set: %v", diags)
		}
		m := baseSloModel()
		m.PageChannelIDs = page
		m.TicketChannelIDs = ticket

		create, gotDiags := sloModelToCreate(context.Background(), &m)
		if gotDiags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", gotDiags)
		}
		if len(create.PageChannelIDs) != 1 || create.PageChannelIDs[0] != "chan-page" {
			t.Fatalf("unexpected page channel seeds: %#v", create.PageChannelIDs)
		}
		if len(create.TicketChannelIDs) != 1 || create.TicketChannelIDs[0] != "chan-ticket" {
			t.Fatalf("unexpected ticket channel seeds: %#v", create.TicketChannelIDs)
		}
	})

	t.Run("maps a histogram_threshold SLI and omits bad_query", func(t *testing.T) {
		t.Parallel()

		m := baseSloModel()
		m.Source = types.StringValue("metrics")
		m.MetricAggregation = types.StringValue("histogram_threshold")
		m.BadQuery = types.StringNull()
		m.Threshold = types.StringValue("60000")
		m.Comparison = types.StringValue("less_than")

		create, diags := sloModelToCreate(context.Background(), &m)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if create.BadQuery != nil {
			t.Fatalf("expected omitted bad_query, got %#v", create.BadQuery)
		}
		if create.Threshold == nil || *create.Threshold != "60000" {
			t.Fatalf("unexpected threshold: %#v", create.Threshold)
		}
		if create.Comparison == nil || *create.Comparison != "less_than" {
			t.Fatalf("unexpected comparison: %#v", create.Comparison)
		}

		// bad_query must be absent from the wire body, not sent as "".
		raw, err := json.Marshal(create)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if _, ok := body["bad_query"]; ok {
			t.Fatalf("expected bad_query absent from body, got %s", raw)
		}
	})

	t.Run("rejects out-of-range windows", func(t *testing.T) {
		t.Parallel()

		for _, window := range []string{"30m", "91d", "bogus"} {
			m := baseSloModel()
			m.RollingWindow = types.StringValue(window)
			if _, diags := sloModelToCreate(context.Background(), &m); !diags.HasError() {
				t.Fatalf("expected diagnostics for window %q", window)
			}
		}
	})
}

func sloRead() *logclient.SloRead {
	return &logclient.SloRead{
		ID:                "slo-1",
		ProjectID:         "proj-1",
		ScopeKind:         "service",
		ScopeValue:        "payments-api",
		Name:              "payments 99.9% / 30d",
		Source:            "records",
		MetricAggregation: "additive",
		TotalQuery:        "parent_span_id IS NULL",
		BadQuery:          "otel_status_code = 'ERROR'",
		TargetPercent:     "99.9000",
		RollingWindow:     "P30D",
		Environments:      []string{},
	}
}

func TestSloReadToModel(t *testing.T) {
	t.Parallel()

	t.Run("keeps configured target and window spellings when equivalent", func(t *testing.T) {
		t.Parallel()

		m := baseSloModel()
		if diags := sloReadToModel(context.Background(), sloRead(), &m); diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if got := m.TargetPercent.ValueString(); got != "99.9" {
			t.Fatalf("expected preserved target 99.9, got %q", got)
		}
		if got := m.RollingWindow.ValueString(); got != "30d" {
			t.Fatalf("expected preserved window 30d, got %q", got)
		}
		if !m.Environments.IsNull() {
			t.Fatalf("expected null environments for empty API list, got %v", m.Environments)
		}
		if !m.Description.IsNull() {
			t.Fatalf("expected null description, got %v", m.Description)
		}
		// Fresh models (import) get a typed null; the API never returns the seeds.
		if !m.PageChannelIDs.IsNull() || m.PageChannelIDs.ElementType(context.Background()) == nil {
			t.Fatalf("expected typed-null page channel seeds, got %v", m.PageChannelIDs)
		}
		if !m.TicketChannelIDs.IsNull() || m.TicketChannelIDs.ElementType(context.Background()) == nil {
			t.Fatalf("expected typed-null ticket channel seeds, got %v", m.TicketChannelIDs)
		}
	})

	t.Run("preserves configured channel seeds the API does not return", func(t *testing.T) {
		t.Parallel()

		page, diags := types.SetValueFrom(context.Background(), types.StringType, []string{"chan-page"})
		if diags.HasError() {
			t.Fatalf("failed to build page channel set: %v", diags)
		}
		m := baseSloModel()
		m.PageChannelIDs = page
		if diags := sloReadToModel(context.Background(), sloRead(), &m); diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		var ids []string
		if diags := m.PageChannelIDs.ElementsAs(context.Background(), &ids, false); diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if len(ids) != 1 || ids[0] != "chan-page" {
			t.Fatalf("unexpected page channel seeds: %#v", ids)
		}
	})

	t.Run("replaces values when the API disagrees", func(t *testing.T) {
		t.Parallel()

		read := sloRead()
		read.TargetPercent = "99.9500"
		read.RollingWindow = "P14D"
		read.Environments = []string{"prod"}
		desc := "desc"
		read.Description = &desc

		m := baseSloModel()
		if diags := sloReadToModel(context.Background(), read, &m); diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if got := m.TargetPercent.ValueString(); got != "99.95" {
			t.Fatalf("expected normalized API target, got %q", got)
		}
		if got := m.RollingWindow.ValueString(); got != "14d" {
			t.Fatalf("expected 14d, got %q", got)
		}
		var envs []string
		if diags := m.Environments.ElementsAs(context.Background(), &envs, false); diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if len(envs) != 1 || envs[0] != "prod" {
			t.Fatalf("unexpected environments: %#v", envs)
		}
		if m.Description.ValueString() != "desc" {
			t.Fatalf("unexpected description: %v", m.Description)
		}
	})

	t.Run("maps histogram threshold/comparison and clears empty bad_query", func(t *testing.T) {
		t.Parallel()

		read := sloRead()
		read.Source = "metrics"
		read.MetricAggregation = "histogram_threshold"
		// Even a stale non-empty bad_query is dropped for this mode.
		read.BadQuery = "otel_status_code = 'ERROR'"
		threshold := "60000.0000"
		comparison := "less_than"
		read.Threshold = &threshold
		read.Comparison = &comparison

		m := baseSloModel()
		m.BadQuery = types.StringNull()
		m.Threshold = types.StringValue("60000")
		m.Comparison = types.StringValue("less_than")
		if diags := sloReadToModel(context.Background(), read, &m); diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if !m.BadQuery.IsNull() {
			t.Fatalf("expected null bad_query, got %v", m.BadQuery)
		}
		if got := m.Threshold.ValueString(); got != "60000" {
			t.Fatalf("expected preserved threshold 60000, got %q", got)
		}
		if got := m.Comparison.ValueString(); got != "less_than" {
			t.Fatalf("expected comparison less_than, got %q", got)
		}
	})

	t.Run("nulls threshold/comparison the API omits", func(t *testing.T) {
		t.Parallel()

		m := baseSloModel()
		m.Threshold = types.StringValue("60000")
		m.Comparison = types.StringValue("less_than")
		if diags := sloReadToModel(context.Background(), sloRead(), &m); diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if !m.Threshold.IsNull() || !m.Comparison.IsNull() {
			t.Fatalf("expected null threshold/comparison, got %v / %v", m.Threshold, m.Comparison)
		}
	})

	t.Run("renders sub-day windows compactly on import", func(t *testing.T) {
		t.Parallel()

		read := sloRead()
		read.RollingWindow = "PT1H30M"
		var m SloModel
		if diags := sloReadToModel(context.Background(), read, &m); diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if got := m.RollingWindow.ValueString(); got != "1h30m" {
			t.Fatalf("expected 1h30m, got %q", got)
		}
	})

	t.Run("normalizes the target on import", func(t *testing.T) {
		t.Parallel()

		var m SloModel
		if diags := sloReadToModel(context.Background(), sloRead(), &m); diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if got := m.TargetPercent.ValueString(); got != "99.9" {
			t.Fatalf("expected normalized target 99.9, got %q", got)
		}
	})
}

func TestSloModelToUpdate(t *testing.T) {
	t.Parallel()

	t.Run("omits every unchanged field", func(t *testing.T) {
		t.Parallel()

		plan := baseSloModel()
		state := baseSloModel()
		plan.Name = types.StringValue("renamed")

		payload, diags := sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Name == nil || *payload.Name != "renamed" {
			t.Fatalf("expected name in payload, got %#v", payload.Name)
		}
		if payload.Description != nil || payload.Source != nil || payload.MetricAggregation != nil ||
			payload.TotalQuery != nil || payload.BadQuery != nil || payload.TargetPercent != nil ||
			payload.RollingWindowSeconds != nil || payload.Environments != nil {
			t.Fatalf("expected only name in payload, got %#v", payload)
		}
	})

	t.Run("treats equivalent target and window spellings as unchanged", func(t *testing.T) {
		t.Parallel()

		plan := baseSloModel()
		state := baseSloModel()
		plan.TargetPercent = types.StringValue("99.90")
		plan.RollingWindow = types.StringValue("720h")

		payload, diags := sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.TargetPercent != nil || payload.RollingWindowSeconds != nil {
			t.Fatalf("expected empty payload for equivalent values, got %#v", payload)
		}
	})

	t.Run("sends changed shape fields", func(t *testing.T) {
		t.Parallel()

		plan := baseSloModel()
		state := baseSloModel()
		plan.TargetPercent = types.StringValue("99.95")
		plan.RollingWindow = types.StringValue("14d")
		plan.BadQuery = types.StringValue("level = 'error'")
		plan.MetricAggregation = types.StringValue("gauge_fraction")
		state.MetricAggregation = types.StringValue("additive")

		payload, diags := sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.TargetPercent == nil || *payload.TargetPercent != "99.95" {
			t.Fatalf("expected target in payload, got %#v", payload.TargetPercent)
		}
		if payload.RollingWindowSeconds == nil || *payload.RollingWindowSeconds != 14*86400 {
			t.Fatalf("expected window in payload, got %#v", payload.RollingWindowSeconds)
		}
		if payload.BadQuery == nil || *payload.BadQuery != "level = 'error'" {
			t.Fatalf("expected bad_query in payload, got %#v", payload.BadQuery)
		}
		if payload.MetricAggregation == nil || *payload.MetricAggregation != "gauge_fraction" {
			t.Fatalf("expected metric_aggregation in payload, got %#v", payload.MetricAggregation)
		}
		if payload.TotalQuery != nil || payload.Name != nil {
			t.Fatalf("expected unchanged fields omitted, got %#v", payload)
		}
	})

	t.Run("environments diff ignores order and clears on removal", func(t *testing.T) {
		t.Parallel()

		mkSet := func(vals ...string) types.Set {
			set, diags := types.SetValueFrom(context.Background(), types.StringType, vals)
			if diags.HasError() {
				t.Fatalf("failed to build set: %v", diags)
			}
			return set
		}

		plan := baseSloModel()
		state := baseSloModel()
		plan.Environments = mkSet("prod", "staging")
		state.Environments = mkSet("staging", "prod")
		payload, diags := sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Environments != nil {
			t.Fatalf("expected same-elements set omitted, got %#v", payload.Environments)
		}

		plan.Environments = types.SetNull(types.StringType)
		payload, diags = sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Environments == nil || len(*payload.Environments) != 0 {
			t.Fatalf("expected empty environments to clear, got %#v", payload.Environments)
		}

		plan.Environments = mkSet("prod")
		payload, diags = sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Environments == nil || len(*payload.Environments) != 1 || (*payload.Environments)[0] != "prod" {
			t.Fatalf("expected changed environments sent, got %#v", payload.Environments)
		}
	})

	t.Run("threshold/comparison tri-state: set, unchanged, and cleared", func(t *testing.T) {
		t.Parallel()

		// null -> value sends the value.
		plan := baseSloModel()
		state := baseSloModel()
		plan.Threshold = types.StringValue("60000")
		plan.Comparison = types.StringValue("less_than")
		payload, diags := sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Threshold == nil || *payload.Threshold == nil || **payload.Threshold != "60000" {
			t.Fatalf("expected threshold value in payload, got %#v", payload.Threshold)
		}
		if payload.Comparison == nil || *payload.Comparison == nil || **payload.Comparison != "less_than" {
			t.Fatalf("expected comparison value in payload, got %#v", payload.Comparison)
		}

		// Equivalent decimal spelling is treated as unchanged (omitted).
		plan.Threshold = types.StringValue("60000.00")
		plan.Comparison = types.StringValue("less_than")
		state.Threshold = types.StringValue("60000")
		state.Comparison = types.StringValue("less_than")
		payload, diags = sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Threshold != nil || payload.Comparison != nil {
			t.Fatalf("expected unchanged threshold/comparison omitted, got %#v / %#v", payload.Threshold, payload.Comparison)
		}

		// value -> null sends an explicit JSON null so the API clears them
		// (e.g. switching a histogram SLO back to another mode).
		plan.Threshold = types.StringNull()
		plan.Comparison = types.StringNull()
		payload, diags = sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Threshold == nil || *payload.Threshold != nil {
			t.Fatalf("expected explicit-null threshold, got %#v", payload.Threshold)
		}
		if payload.Comparison == nil || *payload.Comparison != nil {
			t.Fatalf("expected explicit-null comparison, got %#v", payload.Comparison)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if v, ok := body["threshold"]; !ok || v != nil {
			t.Fatalf("expected threshold:null in body, got %s", raw)
		}
		if v, ok := body["comparison"]; !ok || v != nil {
			t.Fatalf("expected comparison:null in body, got %s", raw)
		}
	})

	t.Run("clears a removed description", func(t *testing.T) {
		t.Parallel()

		plan := baseSloModel()
		state := baseSloModel()
		state.Description = types.StringValue("old")

		payload, diags := sloModelToUpdate(context.Background(), &plan, &state)
		if diags != nil && diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		if payload.Description == nil || *payload.Description != "" {
			t.Fatalf("expected empty description to clear, got %#v", payload.Description)
		}
	})
}

func TestValidateSloSliConfig(t *testing.T) {
	t.Parallel()

	histogram := func() SloModel {
		m := baseSloModel()
		m.Source = types.StringValue("metrics")
		m.MetricAggregation = types.StringValue("histogram_threshold")
		m.BadQuery = types.StringNull()
		m.Threshold = types.StringValue("60000")
		m.Comparison = types.StringValue("less_than")
		return m
	}

	cases := []struct {
		name    string
		mutate  func(*SloModel)
		wantErr bool
	}{
		{"records slo with bad_query is valid", func(m *SloModel) {}, false},
		{"records slo missing bad_query is rejected", func(m *SloModel) { m.BadQuery = types.StringNull() }, true},
		{"records slo with stray threshold is rejected", func(m *SloModel) { m.Threshold = types.StringValue("1") }, true},
		{"records slo with stray comparison is rejected", func(m *SloModel) { m.Comparison = types.StringValue("less_than") }, true},
		{"valid histogram slo", func(m *SloModel) { *m = histogram() }, false},
		{"histogram without source=metrics is rejected", func(m *SloModel) {
			*m = histogram()
			m.Source = types.StringValue("records")
		}, true},
		{"histogram with default (records) source is rejected", func(m *SloModel) {
			*m = histogram()
			m.Source = types.StringNull()
		}, true},
		{"histogram missing threshold is rejected", func(m *SloModel) {
			*m = histogram()
			m.Threshold = types.StringNull()
		}, true},
		{"histogram missing comparison is rejected", func(m *SloModel) {
			*m = histogram()
			m.Comparison = types.StringNull()
		}, true},
		{"histogram without bad_query is fine", func(m *SloModel) { *m = histogram() }, false},
		{"unknown metric_aggregation defers validation", func(m *SloModel) {
			m.MetricAggregation = types.StringUnknown()
			m.BadQuery = types.StringNull()
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := baseSloModel()
			tc.mutate(&m)
			diags := validateSloSliConfig(&m)
			if diags.HasError() != tc.wantErr {
				t.Fatalf("wantErr=%v, got diags=%v", tc.wantErr, diags)
			}
		})
	}
}

func TestNormalizeDecimalString(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{"99.9000", "99.9"},
		{"99.000", "99"},
		{"99", "99"},
		{"99.95", "99.95"},
	}
	for _, tc := range cases {
		if got := normalizeDecimalString(tc.in); got != tc.want {
			t.Errorf("normalizeDecimalString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDecimalStringsEqual(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want bool
	}{
		{"99.9", "99.9000", true},
		{"99", "99.0", true},
		{"99.9", "99.95", false},
		{"", "99.9", false},
		{"abc", "99.9", false},
	}
	for _, tc := range cases {
		if got := decimalStringsEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("decimalStringsEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSloDurationCompact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		d    time.Duration
		want string
	}{
		{90 * 24 * time.Hour, "90d"},
		{14 * 24 * time.Hour, "14d"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
	}
	for _, tc := range cases {
		if got := sloDurationCompact(tc.d); got != tc.want {
			t.Errorf("sloDurationCompact(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
