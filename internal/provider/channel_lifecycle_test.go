// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func channelTestPlan(t *testing.T, r *ChannelResource, m *ChannelModel) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	raw := tftypes.NewValue(schema.Schema.Type().TerraformType(t.Context()), nil)
	plan := tfsdk.Plan{Schema: schema.Schema, Raw: raw}
	if diags := plan.Set(t.Context(), m); diags.HasError() {
		t.Fatal(diags)
	}
	return plan, tfsdk.State{Schema: schema.Schema, Raw: raw}
}

func TestChannelCreateRetainsStateWhenActiveUpdateFails(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/channels/":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"channel-1","label":"channel","active":true,"config":{"type":"webhook","format":"auto","url":"https://example.com/**********"}}`))
		case req.Method == http.MethodPut && req.URL.Path == "/api/v1/channels/channel-1/":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"detail":"failed activation"}`))
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := logclient.NewAPIClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	r := &ChannelResource{client: client}
	m := ChannelModel{
		ID: types.StringUnknown(), Name: types.StringValue("channel"), Active: types.BoolValue(false),
		Config: &ChannelConfigModel{Type: types.StringValue("webhook"), Format: types.StringValue("auto"), URL: types.StringValue("https://example.com/hook")},
	}
	plan, state := channelTestPlan(t, r, &m)
	response := resource.CreateResponse{State: state}
	r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("expected an error from the active update")
	}
	if response.State.Raw.IsNull() {
		t.Fatal("created channel must remain in state after the follow-up update fails")
	}
	var got ChannelModel
	if diags := response.State.Get(t.Context(), &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.ID.ValueString() != "channel-1" || !got.Active.ValueBool() {
		t.Fatalf("created channel state = %#v, want channel-1 with active=true", got)
	}
	if got.Config.URL.ValueString() != "https://example.com/hook" {
		t.Fatalf("retained URL = %q, want the configured URL", got.Config.URL.ValueString())
	}
}

func TestChannelAgentPromptPlan(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		value       types.Bool
		wantUnknown bool
	}{
		{"omitted uses backend default", types.BoolNull(), true},
		{"explicit false", types.BoolValue(false), false},
		{"explicit true", types.BoolValue(true), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &ChannelResource{}
			m := ChannelModel{
				ID: types.StringUnknown(), Name: types.StringValue("channel"), Active: types.BoolValue(true),
				Config: &ChannelConfigModel{Type: types.StringValue("slack-integration"), InstallID: types.StringValue("install-1"), ChannelID: types.StringValue("C123"), IncludeAgentPrompt: tt.value},
			}
			plan, state := channelTestPlan(t, r, &m)
			proposed, err := tfprotov6.NewDynamicValue(plan.Raw.Type(), plan.Raw)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := tfprotov6.NewDynamicValue(state.Raw.Type(), state.Raw)
			if err != nil {
				t.Fatal(err)
			}
			m.ID = types.StringNull()
			configPlan, _ := channelTestPlan(t, r, &m)
			config, err := tfprotov6.NewDynamicValue(configPlan.Raw.Type(), configPlan.Raw)
			if err != nil {
				t.Fatal(err)
			}
			server := providerserver.NewProtocol6(New("test")())()
			response, err := server.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
				TypeName: "logfire_channel", PriorState: &prior, Config: &config, ProposedNewState: &proposed,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range response.Diagnostics {
				if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatal(diagnostic)
				}
			}
			raw, err := response.PlannedState.Unmarshal(plan.Raw.Type())
			if err != nil {
				t.Fatal(err)
			}
			planned := tfsdk.Plan{Schema: plan.Schema, Raw: raw}
			var got ChannelModel
			if diags := planned.Get(t.Context(), &got); diags.HasError() {
				t.Fatal(diags)
			}
			if got.Config.IncludeAgentPrompt.IsUnknown() != tt.wantUnknown {
				t.Fatalf("planned include_agent_prompt = %v, want unknown=%v", got.Config.IncludeAgentPrompt, tt.wantUnknown)
			}
			if !tt.wantUnknown && !got.Config.IncludeAgentPrompt.Equal(tt.value) {
				t.Fatalf("planned include_agent_prompt = %v, want %v", got.Config.IncludeAgentPrompt, tt.value)
			}
		})
	}
}
