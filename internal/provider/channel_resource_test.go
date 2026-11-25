// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
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

func testAccImportID(name string, attrs ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		resourceState, ok := s.RootModule().Resources[name]
		if !ok || resourceState.Primary == nil {
			return "", fmt.Errorf("resource state not found in root module")
		}
		parts := make([]string, len(attrs))
		for i, attr := range attrs {
			parts[i] = resourceState.Primary.Attributes[attr]
		}
		return strings.Join(parts, "/"), nil
	}
}

func TestAccChannelResource(t *testing.T) {
	t.Parallel()

	channelName := testAccRandName("acc-channel")
	updatedChannelName := fmt.Sprintf("%s-renamed", channelName)
	opsgenieAuthKey := fmt.Sprintf("acc-opsgenie-key-%s", acctest.RandStringFromCharSet(16, acctest.CharSetAlphaNum))

	initialURL := "https://example.com/webhook"
	updatedURL := "https://example.com/webhook/updated"

	attr := func(name string, want knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("logfire_channel.test", tfjsonpath.New(name), want)
	}
	config := func(key, want string) statecheck.StateCheck {
		return statecheck.ExpectKnownValue(
			"logfire_channel.test",
			tfjsonpath.New("config").AtMapKey(key),
			knownvalue.StringExact(want),
		)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,

		Steps: []resource.TestStep{
			{
				Config: testAccChannelResourceWebhookConfig(channelName, "auto", initialURL, false),
				ConfigStateChecks: []statecheck.StateCheck{
					attr("id", knownvalue.NotNull()),
					attr("name", knownvalue.StringExact(channelName)),
					attr("active", knownvalue.Bool(false)),
					config("type", "webhook"),
					config("format", "auto"),
					config("url", initialURL),
				},
			},
			{
				ResourceName:            "logfire_channel.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"config.url"},
				ImportStateIdFunc:       testAccImportID("logfire_channel.test", "id"),
			},
			{
				Config: testAccChannelResourceWebhookConfig(channelName, "auto", initialURL, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				Config: testAccChannelResourceWebhookConfig(updatedChannelName, "slack-blockkit", updatedURL, false),
				ConfigStateChecks: []statecheck.StateCheck{
					attr("name", knownvalue.StringExact(updatedChannelName)),
					attr("active", knownvalue.Bool(false)),
					config("format", "slack-blockkit"),
					config("url", updatedURL),
				},
			},
			{
				Config: testAccChannelResourceOpsgenieConfig(updatedChannelName, opsgenieAuthKey),
				ConfigStateChecks: []statecheck.StateCheck{
					attr("name", knownvalue.StringExact(updatedChannelName)),
					config("type", "opsgenie"),
				},
			},
			{
				Config: testAccChannelResourceWebhookConfig(updatedChannelName, "raw-data", updatedURL),
				ConfigStateChecks: []statecheck.StateCheck{
					attr("name", knownvalue.StringExact(updatedChannelName)),
					config("type", "webhook"),
					config("format", "raw-data"),
					config("url", updatedURL),
				},
			},
		},
	})
}

func testAccChannelResourceWebhookConfig(channelName, format, url string, active ...bool) string {
	activeConfig := ""
	if len(active) > 0 {
		activeConfig = fmt.Sprintf("  active = %t\n", active[0])
	}
	return fmt.Sprintf(`%s

resource "logfire_channel" "test" {
  name = %q
%s

  config {
    type   = "webhook"
    format = %q
    url    = %q
  }
}
`, testAccProviderConfig(), channelName, activeConfig, format, url)
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

func TestIsMaskedChannelSecrets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		check func(string) bool
		value string
		want  bool
	}{
		{name: "webhook masked host", check: isMaskedWebhookURL, value: "https://example.com/**********", want: true},
		{name: "webhook masked host with port", check: isMaskedWebhookURL, value: "https://example.com:8443/**********", want: true},
		{name: "webhook regular url", check: isMaskedWebhookURL, value: "https://example.com/webhook/updated", want: false},
		{name: "webhook query string is not a backend mask", check: isMaskedWebhookURL, value: "https://example.com/**********?q=1", want: false},
		{name: "opsgenie masked with suffix", check: isMaskedSecret, value: "**********1234", want: true},
		{name: "opsgenie fully masked short key", check: isMaskedSecret, value: "**********", want: true},
		{name: "opsgenie plain key", check: isMaskedSecret, value: "GenieKey secret-key-12345", want: false},
		{name: "opsgenie wrong masked length", check: isMaskedSecret, value: "**********12345", want: false},
		{name: "pagerduty masked routing key", check: isMaskedSecret, value: "**********c2d3", want: true},
		{name: "pagerduty plain routing key", check: isMaskedSecret, value: "a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.check(tc.value); got != tc.want {
				t.Fatalf("masked(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestChannelReadKeepsConfiguredSecretsLogfireMasks(t *testing.T) {
	t.Parallel()

	str := types.StringValue
	webhook := func(url string) *logclient.WebhookConfig {
		return &logclient.WebhookConfig{
			ChannelConfigBase: logclient.ChannelConfigBase{Type: "webhook"},
			Format:            stringPtr("auto"),
			URL:               stringPtr(url),
		}
	}
	configuredWebhook := func(url string) *ChannelConfigModel {
		return &ChannelConfigModel{Type: str("webhook"), Format: str("auto"), URL: str(url)}
	}
	webhookURL := func(c *ChannelConfigModel) []string { return []string{c.URL.ValueString()} }

	cases := []struct {
		name     string
		remote   any
		fallback *ChannelConfigModel
		got      func(*ChannelConfigModel) []string
		want     []string
	}{
		{
			name:     "preserves planned webhook url when backend returns masked value",
			remote:   webhook("https://example.com/**********"),
			fallback: configuredWebhook("https://example.com/webhook/updated"),
			got:      webhookURL,
			want:     []string{"https://example.com/webhook/updated"},
		},
		{
			name:     "keeps unmasked webhook url from backend",
			remote:   webhook("https://example.com/webhook/new"),
			fallback: configuredWebhook("https://example.com/webhook/old"),
			got:      webhookURL,
			want:     []string{"https://example.com/webhook/new"},
		},
		{
			name: "preserves planned opsgenie key when backend returns masked value",
			remote: &logclient.OpsgenieConfig{
				ChannelConfigBase: logclient.ChannelConfigBase{Type: "opsgenie"},
				AuthKey:           stringPtr("**********2345"),
			},
			fallback: &ChannelConfigModel{Type: str("opsgenie"), AuthKey: str("GenieKey secret-key-12345")},
			got:      func(c *ChannelConfigModel) []string { return []string{c.AuthKey.ValueString()} },
			want:     []string{"GenieKey secret-key-12345"},
		},
		{
			name: "preserves planned pagerduty routing key and remote region",
			remote: &logclient.PagerdutyConfig{
				ChannelConfigBase: logclient.ChannelConfigBase{Type: "pagerduty"},
				RoutingKey:        stringPtr("**********c2d3"),
				Region:            stringPtr("eu"),
			},
			fallback: &ChannelConfigModel{
				Type:       str("pagerduty"),
				RoutingKey: str("a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3"),
				Region:     str("us"),
			},
			got: func(c *ChannelConfigModel) []string {
				return []string{c.RoutingKey.ValueString(), c.Region.ValueString()}
			},
			want: []string{"a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3", "eu"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.got(roundTripChannelConfig(t, tc.remote, tc.fallback))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("reconciled values = %q, want %q", got, tc.want)
			}
		})
	}
}

func channelConfigToAPI[T any](t *testing.T, model *ChannelConfigModel) *T {
	t.Helper()

	apiConfig, diags := channelConfigModelToAPI(model)
	if diags.HasError() {
		t.Fatalf("mapping model to API returned diagnostics: %v", diags)
	}
	typed, ok := apiConfig.(*T)
	if !ok {
		t.Fatalf("API config type = %T, want %T", apiConfig, typed)
	}
	return typed
}

// roundTripChannelConfig reads a config the way Logfire returns it, as JSON, with prior as the
// config already in state.
func roundTripChannelConfig(t *testing.T, cfg any, prior *ChannelConfigModel) *ChannelConfigModel {
	t.Helper()

	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var read logclient.ChannelRead
	if err := json.Unmarshal(body, &read.Config); err != nil {
		t.Fatal(err)
	}
	channel, diags := channelReadToModel(&read, prior)
	if diags.HasError() {
		t.Fatalf("mapping API to model returned diagnostics: %v", diags)
	}
	return channel.Config
}

func requireString(t *testing.T, name string, got *string, want string) {
	t.Helper()

	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %q", name, got, want)
	}
}

func TestPagerdutyChannelConfigMapping(t *testing.T) {
	t.Parallel()

	model := &ChannelConfigModel{
		Type:       types.StringValue("pagerduty"),
		RoutingKey: types.StringValue("a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3"),
		Region:     types.StringValue("eu"),
	}

	pagerduty := channelConfigToAPI[logclient.PagerdutyConfig](t, model)
	requireString(t, "routing key", pagerduty.RoutingKey, model.RoutingKey.ValueString())
	requireString(t, "region", pagerduty.Region, "eu")

	roundTrip := roundTripChannelConfig(t, pagerduty, nil)
	if !roundTrip.RoutingKey.Equal(model.RoutingKey) {
		t.Fatalf("round-trip routing key = %v, want configured value", roundTrip.RoutingKey)
	}
	if !roundTrip.Region.Equal(model.Region) {
		t.Fatalf("round-trip region = %v, want eu", roundTrip.Region)
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

	pagerduty := channelConfigToAPI[logclient.PagerdutyIntegrationConfig](t, model)
	requireString(t, "install id", pagerduty.InstallID, model.InstallID.ValueString())
	requireString(t, "service id", pagerduty.ServiceID, model.ServiceID.ValueString())

	roundTrip := roundTripChannelConfig(t, pagerduty, nil)
	if !roundTrip.InstallID.Equal(model.InstallID) {
		t.Fatalf("round-trip install id = %v, want configured value", roundTrip.InstallID)
	}
	if !roundTrip.ServiceID.Equal(model.ServiceID) {
		t.Fatalf("round-trip service id = %v, want configured value", roundTrip.ServiceID)
	}
	// The connected channel carries no credential, so nothing here should look like the legacy type.
	if !roundTrip.RoutingKey.IsNull() {
		t.Fatalf("round-trip routing key = %v, want null", roundTrip.RoutingKey)
	}
	if !roundTrip.Region.IsNull() {
		t.Fatalf("round-trip region = %v, want null", roundTrip.Region)
	}
}

func TestSlackIntegrationChannelConfigMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		includePrompt types.Bool
		wantPrompt    *bool
	}{
		{name: "explicit false", includePrompt: types.BoolValue(false), wantPrompt: new(false)},
		{name: "omitted agent prompt stays omitted", includePrompt: types.BoolNull(), wantPrompt: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := &ChannelConfigModel{
				Type:               types.StringValue("slack-integration"),
				InstallID:          types.StringValue("018f9a6a-1234-7890-abcd-ef0123456789"),
				ChannelID:          types.StringValue("C0123456789"),
				IncludeAgentPrompt: tc.includePrompt,
			}

			slack := channelConfigToAPI[logclient.SlackIntegrationConfig](t, model)
			requireString(t, "install id", slack.InstallID, model.InstallID.ValueString())
			requireString(t, "channel id", slack.ChannelID, model.ChannelID.ValueString())
			if !reflect.DeepEqual(slack.IncludeAgentPrompt, tc.wantPrompt) {
				t.Fatalf("include agent prompt = %v, want %v", slack.IncludeAgentPrompt, tc.wantPrompt)
			}

			roundTrip := roundTripChannelConfig(t, slack, nil)
			if !roundTrip.InstallID.Equal(model.InstallID) {
				t.Fatalf("round-trip install id = %v, want configured value", roundTrip.InstallID)
			}
			if !roundTrip.ChannelID.Equal(model.ChannelID) {
				t.Fatalf("round-trip channel id = %v, want configured value", roundTrip.ChannelID)
			}
			if !roundTrip.IncludeAgentPrompt.Equal(tc.includePrompt) {
				t.Fatalf("round-trip include agent prompt = %v, want %v", roundTrip.IncludeAgentPrompt, tc.includePrompt)
			}
		})
	}
}

func TestChannelConfigRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	str := types.StringValue
	installID := str("018f9a6a-1234-7890-abcd-ef0123456789")
	serviceID := str("018f9a6a-4321-7890-abcd-ef0123456789")
	slackChannelID := str("C0123456789")
	recipients := []types.String{str("a@example.com")}
	euURL := str("https://api.eu.opsgenie.com")

	for name, model := range map[string]*ChannelConfigModel{
		"pagerduty-integration missing install_id and service_id": {Type: str("pagerduty-integration")},
		"routing_key on pagerduty-integration": {
			Type:       str("pagerduty-integration"),
			InstallID:  installID,
			ServiceID:  serviceID,
			RoutingKey: str("0123456789abcdef0123456789abcdef"),
		},
		"channel_id on pagerduty-integration": {
			Type:      str("pagerduty-integration"),
			InstallID: installID,
			ServiceID: serviceID,
			ChannelID: slackChannelID,
		},
		"service_id on webhook": {
			Type:      str("webhook"),
			Format:    str("auto"),
			URL:       str("https://example.com/hook"),
			ServiceID: serviceID,
		},
		"service_id on pagerduty": {
			Type:       str("pagerduty"),
			RoutingKey: str("0123456789abcdef0123456789abcdef"),
			ServiceID:  serviceID,
		},
		"service_id on slack-integration": {
			Type:      str("slack-integration"),
			InstallID: installID,
			ChannelID: slackChannelID,
			ServiceID: serviceID,
		},
		"slack-integration missing install_id and channel_id": {Type: str("slack-integration")},
		"url on slack-integration": {
			Type:      str("slack-integration"),
			InstallID: installID,
			ChannelID: slackChannelID,
			URL:       str("https://example.com"),
		},
		"install_id on webhook": {
			Type:      str("webhook"),
			Format:    str("auto"),
			URL:       str("https://example.com"),
			InstallID: installID,
		},
		"include_agent_prompt on pagerduty": {
			Type:               str("pagerduty"),
			RoutingKey:         str("a0b1c2d3e4f5a0b1c2d3e4f5a0b1c2d3"),
			IncludeAgentPrompt: types.BoolValue(true),
		},
		"email without recipients": {Type: str("email")},
		"url on email":             {Type: str("email"), Recipients: recipients, URL: str("https://example.com")},
		"base_url on webhook": {
			Type:    str("webhook"),
			Format:  str("auto"),
			URL:     str("https://example.com"),
			BaseURL: euURL,
		},
		"recipients on opsgenie":  {Type: str("opsgenie"), AuthKey: str("key"), Recipients: recipients},
		"recipients on pagerduty": {Type: str("pagerduty"), RoutingKey: str("key"), Recipients: recipients},
		"base_url on slack-integration": {
			Type:      str("slack-integration"),
			InstallID: str("install"),
			ChannelID: str("C1"),
			BaseURL:   euURL,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, diags := channelConfigModelToAPI(model); !diags.HasError() {
				t.Fatal("expected diagnostics")
			}
		})
	}
}

func TestSameEmail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		configured, stored string
		same               bool
	}{
		{"a@Example.COM", "a@example.com", true},
		{" Support@example.com", "support@example.com", true},
		{"oncall@example。com", "oncall@example.com", true},
		{"b@xn--bcher-kva.example", "b@bücher.example", true},
		{"Ops@example.com", "ops@example.com", false},
		// email-validator lowercases only an ASCII mailbox name. NFC turns the Kelvin sign
		// (U+212A) into K, which it keeps.
		{"marKeting@example.com", "marketing@example.com", false},
		{"marKeting@example.com", "marKeting@example.com", true},
	} {
		if got := canonicalEmail(tc.configured) == tc.stored; got != tc.same {
			t.Errorf("canonicalEmail(%q) == %q = %v; want %v", tc.configured, tc.stored, got, tc.same)
		}
	}
}
