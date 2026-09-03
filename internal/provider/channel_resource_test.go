// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestAccChannelResource(t *testing.T) {
	t.Parallel()

	channelName := fmt.Sprintf("acc-channel-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))
	updatedChannelName := fmt.Sprintf("%s-renamed", channelName)
	opsgenieAuthKey := fmt.Sprintf("acc-opsgenie-key-%s", acctest.RandStringFromCharSet(16, acctest.CharSetAlphaNum))

	initialURL := "https://example.com/webhook"
	updatedURL := "https://example.com/webhook/updated"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,

		Steps: []resource.TestStep{
			{
				Config: testAccChannelResourceWebhookConfig(channelName, "auto", initialURL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(channelName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("active"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("type"), knownvalue.StringExact("webhook")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("format"), knownvalue.StringExact("auto")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("url"), knownvalue.StringExact(initialURL)),
				},
			},
			{
				ResourceName:      "logfire_channel.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"config.url",
				},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_channel.test"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					id := resourceState.Primary.Attributes["id"]
					return id, nil
				},
			},
			{
				Config: testAccChannelResourceWebhookConfig(channelName, "auto", initialURL),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccChannelResourceWebhookConfig(updatedChannelName, "slack-blockkit", updatedURL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(updatedChannelName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("format"), knownvalue.StringExact("slack-blockkit")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("url"), knownvalue.StringExact(updatedURL)),
				},
			},
			{
				Config: testAccChannelResourceOpsgenieConfig(updatedChannelName, opsgenieAuthKey),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(updatedChannelName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("type"), knownvalue.StringExact("opsgenie")),
				},
			},
			{
				Config: testAccChannelResourceWebhookConfig(updatedChannelName, "raw-data", updatedURL),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("name"), knownvalue.StringExact(updatedChannelName)),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("type"), knownvalue.StringExact("webhook")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("format"), knownvalue.StringExact("raw-data")),
					statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New("config").AtMapKey("url"), knownvalue.StringExact(updatedURL)),
				},
			},
		},
	})
}

func testAccChannelResourceWebhookConfig(channelName, format, url string) string {
	return fmt.Sprintf(`%s

resource "logfire_channel" "test" {
  name = %q

  config {
    type   = "webhook"
    format = %q
    url    = %q
  }
}
`, testAccProviderConfig(), channelName, format, url)
}

func testAccChannelResourceOpsgenieConfig(channelName, authKey string) string {
	return fmt.Sprintf(`%s

resource "logfire_channel" "test" {
  name = %q

  config {
    type     = "opsgenie"
    auth_key = %q
  }
}
`, testAccProviderConfig(), channelName, authKey)
}

func TestIsMaskedWebhookURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "masked host", value: "https://example.com/**********", want: true},
		{name: "masked host with port", value: "https://example.com:8443/**********", want: true},
		{name: "regular webhook url", value: "https://example.com/webhook/updated", want: false},
		{name: "query string is not a backend mask", value: "https://example.com/**********?q=1", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isMaskedWebhookURL(tc.value); got != tc.want {
				t.Fatalf("isMaskedWebhookURL(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestIsMaskedOpsgenieAuthKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "masked with suffix", value: "**********1234", want: true},
		{name: "fully masked short key", value: "**********", want: true},
		{name: "plain key", value: "GenieKey secret-key-12345", want: false},
		{name: "wrong masked length", value: "**********12345", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isMaskedOpsgenieAuthKey(tc.value); got != tc.want {
				t.Fatalf("isMaskedOpsgenieAuthKey(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestIsMaskedPagerdutyRoutingKey(t *testing.T) {
	t.Parallel()

	if !isMaskedPagerdutyRoutingKey("**********c2d3") {
		t.Fatal("expected masked PagerDuty routing key to be detected")
	}
	if isMaskedPagerdutyRoutingKey("a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3") {
		t.Fatal("expected plain PagerDuty routing key not to be detected")
	}
}

func TestReconcileChannelConfigMaskedSecrets(t *testing.T) {
	t.Parallel()

	t.Run("preserves planned webhook url when backend returns masked value", func(t *testing.T) {
		t.Parallel()

		remote := &ChannelConfigModel{
			Type:   types.StringValue("webhook"),
			Format: types.StringValue("auto"),
			URL:    types.StringValue("https://example.com/**********"),
		}
		fallback := &ChannelConfigModel{
			Type:   types.StringValue("webhook"),
			Format: types.StringValue("auto"),
			URL:    types.StringValue("https://example.com/webhook/updated"),
		}

		got := reconcileChannelConfigMaskedSecrets(remote, fallback)
		if got.URL.ValueString() != fallback.URL.ValueString() {
			t.Fatalf("webhook url = %q, want %q", got.URL.ValueString(), fallback.URL.ValueString())
		}
	})

	t.Run("keeps unmasked webhook url from backend", func(t *testing.T) {
		t.Parallel()

		remote := &ChannelConfigModel{
			Type:   types.StringValue("webhook"),
			Format: types.StringValue("auto"),
			URL:    types.StringValue("https://example.com/webhook/new"),
		}
		fallback := &ChannelConfigModel{
			Type:   types.StringValue("webhook"),
			Format: types.StringValue("auto"),
			URL:    types.StringValue("https://example.com/webhook/old"),
		}

		got := reconcileChannelConfigMaskedSecrets(remote, fallback)
		if got.URL.ValueString() != remote.URL.ValueString() {
			t.Fatalf("webhook url = %q, want %q", got.URL.ValueString(), remote.URL.ValueString())
		}
	})

	t.Run("preserves planned opsgenie key when backend returns masked value", func(t *testing.T) {
		t.Parallel()

		remote := &ChannelConfigModel{
			Type:    types.StringValue("opsgenie"),
			AuthKey: types.StringValue("**********2345"),
		}
		fallback := &ChannelConfigModel{
			Type:    types.StringValue("opsgenie"),
			AuthKey: types.StringValue("GenieKey secret-key-12345"),
		}

		got := reconcileChannelConfigMaskedSecrets(remote, fallback)
		if got.AuthKey.ValueString() != fallback.AuthKey.ValueString() {
			t.Fatalf("opsgenie key = %q, want %q", got.AuthKey.ValueString(), fallback.AuthKey.ValueString())
		}
	})

	t.Run("preserves planned pagerduty routing key and remote region", func(t *testing.T) {
		t.Parallel()

		remote := &ChannelConfigModel{
			Type:       types.StringValue("pagerduty"),
			RoutingKey: types.StringValue("**********c2d3"),
			Region:     types.StringValue("eu"),
		}
		fallback := &ChannelConfigModel{
			Type:       types.StringValue("pagerduty"),
			RoutingKey: types.StringValue("a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3"),
			Region:     types.StringValue("us"),
		}

		got := reconcileChannelConfigMaskedSecrets(remote, fallback)
		if got.RoutingKey.ValueString() != fallback.RoutingKey.ValueString() {
			t.Fatalf("pagerduty routing key = %q, want configured value", got.RoutingKey.ValueString())
		}
		if got.Region.ValueString() != remote.Region.ValueString() {
			t.Fatalf("pagerduty region = %q, want %q", got.Region.ValueString(), remote.Region.ValueString())
		}
	})
}

func TestPagerdutyChannelConfigMapping(t *testing.T) {
	t.Parallel()

	model := &ChannelConfigModel{
		Type:       types.StringValue("pagerduty"),
		Format:     types.StringNull(),
		URL:        types.StringNull(),
		AuthKey:    types.StringNull(),
		RoutingKey: types.StringValue("a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3"),
		Region:     types.StringValue("eu"),
	}

	apiConfig, diags := channelConfigModelToAPI(model)
	if diags.HasError() {
		t.Fatalf("mapping model to API returned diagnostics: %v", diags)
	}
	pagerduty, ok := apiConfig.(*logclient.PagerdutyConfig)
	if !ok {
		t.Fatalf("API config type = %T, want *client.PagerdutyConfig", apiConfig)
	}
	if pagerduty.RoutingKey == nil || *pagerduty.RoutingKey != model.RoutingKey.ValueString() {
		t.Fatalf("routing key = %v, want configured value", pagerduty.RoutingKey)
	}
	if pagerduty.Region == nil || *pagerduty.Region != "eu" {
		t.Fatalf("region = %v, want eu", pagerduty.Region)
	}

	roundTrip, roundTripDiags := channelConfigAPIToModel(pagerduty)
	if roundTripDiags.HasError() {
		t.Fatalf("mapping API to model returned diagnostics: %v", roundTripDiags)
	}
	if roundTrip.RoutingKey.ValueString() != model.RoutingKey.ValueString() {
		t.Fatalf("round-trip routing key = %q, want configured value", roundTrip.RoutingKey.ValueString())
	}
	if roundTrip.Region.ValueString() != "eu" {
		t.Fatalf("round-trip region = %q, want eu", roundTrip.Region.ValueString())
	}
	if !roundTrip.ServiceID.IsNull() {
		t.Fatalf("round-trip service id = %v, want null", roundTrip.ServiceID)
	}
}

func TestPagerdutyIntegrationChannelConfigMapping(t *testing.T) {
	t.Parallel()

	model := &ChannelConfigModel{
		Type:      types.StringValue("pagerduty-integration"),
		InstallID: types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
		ServiceID: types.StringValue("018f9a6a-4321-7890-abcd-ef0123456789"),
	}

	apiConfig, diags := channelConfigModelToAPI(model)
	if diags.HasError() {
		t.Fatalf("mapping model to API returned diagnostics: %v", diags)
	}
	pagerduty, ok := apiConfig.(*logclient.PagerdutyIntegrationConfig)
	if !ok {
		t.Fatalf("API config type = %T, want *client.PagerdutyIntegrationConfig", apiConfig)
	}
	if pagerduty.InstallID == nil || *pagerduty.InstallID != model.InstallID.ValueString() {
		t.Fatalf("install id = %v, want configured value", pagerduty.InstallID)
	}
	if pagerduty.ServiceID == nil || *pagerduty.ServiceID != model.ServiceID.ValueString() {
		t.Fatalf("service id = %v, want configured value", pagerduty.ServiceID)
	}

	roundTrip, roundTripDiags := channelConfigAPIToModel(pagerduty)
	if roundTripDiags.HasError() {
		t.Fatalf("mapping API to model returned diagnostics: %v", roundTripDiags)
	}
	if roundTrip.InstallID.ValueString() != model.InstallID.ValueString() {
		t.Fatalf("round-trip install id = %q, want configured value", roundTrip.InstallID.ValueString())
	}
	if roundTrip.ServiceID.ValueString() != model.ServiceID.ValueString() {
		t.Fatalf("round-trip service id = %q, want configured value", roundTrip.ServiceID.ValueString())
	}
	// The connected channel carries no credential, so nothing here should look like the legacy type.
	if !roundTrip.RoutingKey.IsNull() {
		t.Fatalf("round-trip routing key = %v, want null", roundTrip.RoutingKey)
	}
	if !roundTrip.Region.IsNull() {
		t.Fatalf("round-trip region = %v, want null", roundTrip.Region)
	}
}

func TestPagerdutyIntegrationChannelConfigValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing install_id and service_id", func(t *testing.T) {
		model := &ChannelConfigModel{
			Type: types.StringValue("pagerduty-integration"),
		}
		_, diags := channelConfigModelToAPI(model)
		if !diags.HasError() {
			t.Fatal("expected diagnostics for missing install_id and service_id")
		}
	})

	t.Run("routing key rejected", func(t *testing.T) {
		model := &ChannelConfigModel{
			Type:       types.StringValue("pagerduty-integration"),
			InstallID:  types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
			ServiceID:  types.StringValue("018f9a6a-4321-7890-abcd-ef0123456789"),
			RoutingKey: types.StringValue("0123456789abcdef0123456789abcdef"),
		}
		_, diags := channelConfigModelToAPI(model)
		if !diags.HasError() {
			t.Fatal("expected diagnostics for routing_key on pagerduty-integration channel")
		}
	})

	t.Run("slack fields rejected", func(t *testing.T) {
		model := &ChannelConfigModel{
			Type:      types.StringValue("pagerduty-integration"),
			InstallID: types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
			ServiceID: types.StringValue("018f9a6a-4321-7890-abcd-ef0123456789"),
			ChannelID: types.StringValue("C0123456789"),
		}
		_, diags := channelConfigModelToAPI(model)
		if !diags.HasError() {
			t.Fatal("expected diagnostics for channel_id on pagerduty-integration channel")
		}
	})

	t.Run("service_id rejected on other types", func(t *testing.T) {
		for _, model := range []*ChannelConfigModel{
			{
				Type:      types.StringValue("webhook"),
				Format:    types.StringValue("auto"),
				URL:       types.StringValue("https://example.com/hook"),
				ServiceID: types.StringValue("018f9a6a-4321-7890-abcd-ef0123456789"),
			},
			{
				Type:       types.StringValue("pagerduty"),
				RoutingKey: types.StringValue("0123456789abcdef0123456789abcdef"),
				ServiceID:  types.StringValue("018f9a6a-4321-7890-abcd-ef0123456789"),
			},
			{
				Type:      types.StringValue("slack-integration"),
				InstallID: types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
				ChannelID: types.StringValue("C0123456789"),
				ServiceID: types.StringValue("018f9a6a-4321-7890-abcd-ef0123456789"),
			},
		} {
			if _, diags := channelConfigModelToAPI(model); !diags.HasError() {
				t.Fatalf("expected diagnostics for service_id on %s channel", model.Type.ValueString())
			}
		}
	})
}

func TestSlackIntegrationChannelConfigMapping(t *testing.T) {
	t.Parallel()

	model := &ChannelConfigModel{
		Type:               types.StringValue("slack-integration"),
		Format:             types.StringNull(),
		URL:                types.StringNull(),
		AuthKey:            types.StringNull(),
		RoutingKey:         types.StringNull(),
		Region:             types.StringNull(),
		InstallID:          types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
		ChannelID:          types.StringValue("C0123456789"),
		IncludeAgentPrompt: types.BoolValue(false),
	}

	apiConfig, diags := channelConfigModelToAPI(model)
	if diags.HasError() {
		t.Fatalf("mapping model to API returned diagnostics: %v", diags)
	}
	slack, ok := apiConfig.(*logclient.SlackIntegrationConfig)
	if !ok {
		t.Fatalf("API config type = %T, want *client.SlackIntegrationConfig", apiConfig)
	}
	if slack.InstallID == nil || *slack.InstallID != model.InstallID.ValueString() {
		t.Fatalf("install id = %v, want configured value", slack.InstallID)
	}
	if slack.ChannelID == nil || *slack.ChannelID != model.ChannelID.ValueString() {
		t.Fatalf("channel id = %v, want configured value", slack.ChannelID)
	}
	if slack.IncludeAgentPrompt == nil || *slack.IncludeAgentPrompt != false {
		t.Fatalf("include agent prompt = %v, want false", slack.IncludeAgentPrompt)
	}

	roundTrip, roundTripDiags := channelConfigAPIToModel(slack)
	if roundTripDiags.HasError() {
		t.Fatalf("mapping API to model returned diagnostics: %v", roundTripDiags)
	}
	if roundTrip.InstallID.ValueString() != model.InstallID.ValueString() {
		t.Fatalf("round-trip install id = %q, want configured value", roundTrip.InstallID.ValueString())
	}
	if roundTrip.ChannelID.ValueString() != model.ChannelID.ValueString() {
		t.Fatalf("round-trip channel id = %q, want configured value", roundTrip.ChannelID.ValueString())
	}
	if roundTrip.IncludeAgentPrompt.ValueBool() != false {
		t.Fatalf("round-trip include agent prompt = %v, want false", roundTrip.IncludeAgentPrompt.ValueBool())
	}
}

func TestSlackIntegrationChannelConfigOmittedAgentPrompt(t *testing.T) {
	t.Parallel()

	model := &ChannelConfigModel{
		Type:               types.StringValue("slack-integration"),
		Format:             types.StringNull(),
		URL:                types.StringNull(),
		AuthKey:            types.StringNull(),
		RoutingKey:         types.StringNull(),
		Region:             types.StringNull(),
		InstallID:          types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
		ChannelID:          types.StringValue("C0123456789"),
		IncludeAgentPrompt: types.BoolNull(),
	}

	apiConfig, diags := channelConfigModelToAPI(model)
	if diags.HasError() {
		t.Fatalf("mapping model to API returned diagnostics: %v", diags)
	}
	slack, ok := apiConfig.(*logclient.SlackIntegrationConfig)
	if !ok {
		t.Fatalf("API config type = %T, want *client.SlackIntegrationConfig", apiConfig)
	}
	if slack.IncludeAgentPrompt != nil {
		t.Fatalf("include agent prompt = %v, want omitted (nil)", slack.IncludeAgentPrompt)
	}

	roundTrip, roundTripDiags := channelConfigAPIToModel(slack)
	if roundTripDiags.HasError() {
		t.Fatalf("mapping API to model returned diagnostics: %v", roundTripDiags)
	}
	if !roundTrip.IncludeAgentPrompt.IsNull() {
		t.Fatalf("round-trip include agent prompt = %v, want null", roundTrip.IncludeAgentPrompt)
	}
}

func TestSlackIntegrationChannelConfigValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing install_id and channel_id", func(t *testing.T) {
		model := &ChannelConfigModel{
			Type: types.StringValue("slack-integration"),
		}
		_, diags := channelConfigModelToAPI(model)
		if !diags.HasError() {
			t.Fatal("expected diagnostics for missing install_id and channel_id")
		}
	})

	t.Run("webhook fields rejected", func(t *testing.T) {
		model := &ChannelConfigModel{
			Type:      types.StringValue("slack-integration"),
			InstallID: types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
			ChannelID: types.StringValue("C0123456789"),
			URL:       types.StringValue("https://example.com"),
		}
		_, diags := channelConfigModelToAPI(model)
		if !diags.HasError() {
			t.Fatal("expected diagnostics for url on slack-integration channel")
		}
	})

	t.Run("slack fields rejected on webhook", func(t *testing.T) {
		model := &ChannelConfigModel{
			Type:      types.StringValue("webhook"),
			Format:    types.StringValue("auto"),
			URL:       types.StringValue("https://example.com"),
			InstallID: types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
		}
		_, diags := channelConfigModelToAPI(model)
		if !diags.HasError() {
			t.Fatal("expected diagnostics for install_id on webhook channel")
		}
	})

	t.Run("include_agent_prompt rejected on pagerduty", func(t *testing.T) {
		model := &ChannelConfigModel{
			Type:               types.StringValue("pagerduty"),
			RoutingKey:         types.StringValue("a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3"),
			IncludeAgentPrompt: types.BoolValue(true),
		}
		_, diags := channelConfigModelToAPI(model)
		if !diags.HasError() {
			t.Fatal("expected diagnostics for include_agent_prompt on pagerduty channel")
		}
	})
}
