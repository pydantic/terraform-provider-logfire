// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/pydantic/terraform-provider-logfire/internal/provider"
)

// State that lacks allow_ui_edits holds null for it. The plan must differ
// from that state, or Terraform makes no API call and the dashboard never
// gets marked as managed.
func TestDashboardStateWithoutAllowUIEditsPlansUpdate(t *testing.T) {
	t.Parallel()
	const definition = `{\"kind\":\"Dashboard\",\"metadata\":{},\"spec\":{}}`
	var schema resource.SchemaResponse
	provider.NewDashboardResource().Schema(t.Context(), resource.SchemaRequest{}, &schema)
	objectType := schema.Schema.Type().TerraformType(t.Context())
	server := providerserver.NewProtocol6(provider.New("test")())()

	upgraded, err := server.UpgradeResourceState(t.Context(), &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "logfire_dashboard",
		RawState: &tfprotov6.RawState{JSON: []byte(`{"id":"dashboard-1","project_id":"project-1","name":"Main","slug":"main","definition":"` + definition + `"}`)},
	})
	if err != nil || len(upgraded.Diagnostics) != 0 {
		t.Fatalf("upgrade failed: %v %v", err, upgraded.Diagnostics)
	}
	prior, err := upgraded.UpgradedState.Unmarshal(objectType)
	if err != nil {
		t.Fatal(err)
	}
	var priorAttributes map[string]tftypes.Value
	if err := prior.As(&priorAttributes); err != nil {
		t.Fatal(err)
	}
	if !priorAttributes["allow_ui_edits"].IsNull() {
		t.Fatalf("prior allow_ui_edits = %v, want null", priorAttributes["allow_ui_edits"])
	}

	// The configuration omits allow_ui_edits, so Terraform proposes the prior state.
	planned, err := server.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "logfire_dashboard",
		PriorState:       upgraded.UpgradedState,
		ProposedNewState: upgraded.UpgradedState,
		Config:           upgraded.UpgradedState,
	})
	if err != nil || len(planned.Diagnostics) != 0 {
		t.Fatalf("plan failed: %v %v", err, planned.Diagnostics)
	}
	plan, err := planned.PlannedState.Unmarshal(objectType)
	if err != nil {
		t.Fatal(err)
	}
	var planAttributes map[string]tftypes.Value
	if err := plan.As(&planAttributes); err != nil {
		t.Fatal(err)
	}
	var allow bool
	if err := planAttributes["allow_ui_edits"].As(&allow); err != nil || allow {
		t.Fatalf("planned allow_ui_edits = %v, want false", planAttributes["allow_ui_edits"])
	}
	if len(planned.RequiresReplace) != 0 {
		t.Fatalf("plan replaces the dashboard: %v", planned.RequiresReplace)
	}
}
