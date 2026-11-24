// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	objectvalidator "github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	setvalidator "github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	stringvalidator "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"
)

type ChannelResource struct{ configured }

const maskedChannelSecretPlaceholder = "**********"

type ChannelConfigModel struct {
	// Type is webhook, email, opsgenie, pagerduty, pagerduty-integration, or
	// slack-integration.
	Type               types.String   `tfsdk:"type"`
	Format             types.String   `tfsdk:"format"`
	URL                types.String   `tfsdk:"url"`
	Recipients         []types.String `tfsdk:"recipients"`
	AuthKey            types.String   `tfsdk:"auth_key"`
	BaseURL            types.String   `tfsdk:"base_url"`
	RoutingKey         types.String   `tfsdk:"routing_key"`
	Region             types.String   `tfsdk:"region"`
	InstallID          types.String   `tfsdk:"install_id"`
	ChannelID          types.String   `tfsdk:"channel_id"`
	ServiceID          types.String   `tfsdk:"service_id"`
	IncludeAgentPrompt types.Bool     `tfsdk:"include_agent_prompt"`
}

type ChannelModel struct {
	ID     types.String        `tfsdk:"id"`
	Name   types.String        `tfsdk:"name"`
	Active types.Bool          `tfsdk:"active"`
	Config *ChannelConfigModel `tfsdk:"config"`
}

var channelConfigModelToAPIMappers = map[string]func(*ChannelConfigModel) (any, diag.Diagnostics){
	"webhook":               webhookConfigModelToAPI,
	"email":                 emailConfigModelToAPI,
	"opsgenie":              opsgenieConfigModelToAPI,
	"pagerduty":             pagerdutyConfigModelToAPI,
	"pagerduty-integration": pagerdutyIntegrationConfigModelToAPI,
	"slack-integration":     slackIntegrationConfigModelToAPI,
}

func (r *ChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_channel"
}

func (r *ChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "Manages a Logfire alert channel. Import accepts the channel UUID or its name (label).",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Channel ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Channel name.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"active": rschema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the channel is active.",
				Default:             booldefault.StaticBool(true),
			},
		},
		Blocks: map[string]rschema.Block{
			"config": rschema.SingleNestedBlock{
				MarkdownDescription: "Required channel configuration.",
				Validators: []validator.Object{
					objectvalidator.IsRequired(),
				},
				Attributes: map[string]rschema.Attribute{
					"type": rschema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Channel type (`webhook`, `email`, `opsgenie`, `pagerduty`, `pagerduty-integration`, or `slack-integration`).",
						Validators: []validator.String{
							stringvalidator.OneOf(
								"webhook", "email", "opsgenie", "pagerduty",
								"pagerduty-integration", "slack-integration",
							),
						},
					},
					"format": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Webhook payload format.",
						Validators: []validator.String{
							stringvalidator.OneOf(
								"auto", "slack-blockkit", "slack-legacy", "raw-data",
							),
						},
					},
					"url": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Webhook URL endpoint.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(
								regexp.MustCompile(`^https?://`),
								"must be a valid HTTP or HTTPS URL",
							),
						},
					},
					"recipients": rschema.SetAttribute{
						Optional:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Email addresses the channel notifies, 1 to 50. Required for `email` channels, which need a Logfire plan with custom email recipients. Logfire normalizes each address, for example by lowercasing its domain. The provider keeps the configured spelling when Logfire stores an equivalent one. A recipient who unsubscribes leaves the channel, and Logfire rejects an update that adds them back until you remove the address from the configuration.",
						Validators: []validator.Set{
							setvalidator.SizeBetween(1, 50),
							// Logfire trims an address with Python's strip(), which also removes
							// control characters that Go keeps, such as U+001F. Logfire rejects
							// them inside an address, so no valid address has one.
							setvalidator.ValueStringsAre(stringvalidator.RegexMatches(
								regexp.MustCompile(`^[^\p{Cc}]+@[^\p{Cc}]+$`),
								"must be an email address without control characters",
							)),
						},
					},
					"auth_key": rschema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "Opsgenie API key.",
					},
					"base_url": rschema.StringAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Opsgenie API base URL, for example `https://api.eu.opsgenie.com` for an EU account. Only for `opsgenie` channels. Omitting it on an existing channel retains the current value; set it to `https://api.opsgenie.com` explicitly to return to the default US endpoint.",
						Validators: []validator.String{
							// The provider trims the URL it sends, so surrounding spaces would
							// come back changed and fail the apply. \p{Cc} and \p{Z} cover every
							// character that Go and Python trim, including a vertical tab and
							// U+0085.
							stringvalidator.RegexMatches(
								regexp.MustCompile(`^https://[^\p{Cc}\p{Z}]+$`),
								"must be an HTTPS URL without spaces or control characters",
							),
						},
					},
					"routing_key": rschema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "PagerDuty Events API v2 integration routing key. Only for `pagerduty` channels; `pagerduty-integration` channels resolve the key from the installation instead.",
					},
					"region": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "PagerDuty account region (`us` or `eu`). When omitted, Logfire uses the US Events API endpoint. Only for `pagerduty` channels; a `pagerduty-integration` channel takes its region from the connected account.",
						Validators: []validator.String{
							stringvalidator.OneOf("us", "eu"),
						},
					},
					"install_id": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "ID of the organization's Slack App or PagerDuty App installation, created by connecting the platform in the Logfire UI (Organization Settings -> Connections). The installation must belong to the same organization, be active, and match the channel type.",
					},
					"channel_id": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Slack channel ID (for example `C0123456789`) the notifications are posted to. The Logfire Slack bot must already be a member of the channel.",
					},
					"service_id": rschema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Logfire's ID (a UUID) for the PagerDuty service incidents are opened on, as approved for the installation when connecting PagerDuty. This is not PagerDuty's own service ID. Required for `pagerduty-integration` channels.",
					},
					"include_agent_prompt": rschema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Whether Slack issue notifications include the \"Ask your agent\" MCP prompt line. Defaults to `true` for new Slack channels. Omitting it on an existing channel retains the current setting; set it to `true` explicitly to re-enable the prompt.",
					},
				},
			},
		},
	}
}

func channelConfigModelToAPI(m *ChannelConfigModel) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	cfgType := m.Type.ValueString()
	if cfgType != "opsgenie" {
		diags.Append(disallowConfigString(m.BaseURL, "config.base_url", cfgType)...)
	}
	if cfgType != "email" && len(m.Recipients) > 0 {
		diags.AddError(
			"Invalid channel config",
			fmt.Sprintf("config.recipients cannot be set for %s channels.", cfgType),
		)
	}
	if diags.HasError() {
		return nil, diags
	}
	return channelConfigModelToAPIMappers[cfgType](m)
}

func webhookConfigModelToAPI(m *ChannelConfigModel) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	format := requiredConfigString(&diags, m.Format, "config.format", "webhook")
	webhookURL := requiredConfigString(&diags, m.URL, "config.url", "webhook")
	diags.Append(disallowConfigFields(m, "webhook", "format", "url")...)
	if diags.HasError() {
		return nil, diags
	}
	return &logclient.WebhookConfig{
		ChannelConfigBase: logclient.ChannelConfigBase{Type: "webhook"},
		Format:            &format,
		URL:               &webhookURL,
	}, nil
}

func emailConfigModelToAPI(m *ChannelConfigModel) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if len(m.Recipients) == 0 {
		diags.AddError(
			"Invalid channel config", "config.recipients must be set for email channels.",
		)
	}
	diags.Append(disallowConfigFields(m, "email")...)
	if diags.HasError() {
		return nil, diags
	}
	recipients := make([]string, len(m.Recipients))
	for i, r := range m.Recipients {
		recipients[i] = r.ValueString()
	}
	// Sorted so that Update can compare two configs.
	slices.Sort(recipients)
	return &logclient.EmailConfig{
		ChannelConfigBase: logclient.ChannelConfigBase{Type: "email"},
		Recipients:        recipients,
	}, nil
}

func opsgenieConfigModelToAPI(m *ChannelConfigModel) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	key := requiredConfigString(&diags, m.AuthKey, "config.auth_key", "opsgenie")
	diags.Append(disallowConfigFields(m, "opsgenie", "auth_key")...)
	if diags.HasError() {
		return nil, diags
	}
	return &logclient.OpsgenieConfig{
		ChannelConfigBase: logclient.ChannelConfigBase{Type: "opsgenie"},
		AuthKey:           &key,
		BaseURL:           terraformStringPointer(m.BaseURL),
	}, nil
}

func pagerdutyConfigModelToAPI(m *ChannelConfigModel) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	routingKey := requiredConfigString(&diags, m.RoutingKey, "config.routing_key", "pagerduty")
	diags.Append(disallowConfigFields(m, "pagerduty", "routing_key", "region")...)
	if diags.HasError() {
		return nil, diags
	}
	return &logclient.PagerdutyConfig{
		ChannelConfigBase: logclient.ChannelConfigBase{Type: "pagerduty"},
		RoutingKey:        &routingKey,
		Region:            terraformStringPointer(m.Region),
	}, nil
}

func pagerdutyIntegrationConfigModelToAPI(m *ChannelConfigModel) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	const channelType = "pagerduty-integration"
	installID := requiredConfigString(&diags, m.InstallID, "config.install_id", channelType)
	serviceID := requiredConfigString(&diags, m.ServiceID, "config.service_id", channelType)
	diags.Append(disallowConfigFields(m, channelType, "install_id", "service_id")...)
	if diags.HasError() {
		return nil, diags
	}
	return &logclient.PagerdutyIntegrationConfig{
		ChannelConfigBase: logclient.ChannelConfigBase{Type: channelType},
		InstallID:         &installID,
		ServiceID:         &serviceID,
	}, nil
}

func slackIntegrationConfigModelToAPI(m *ChannelConfigModel) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	const channelType = "slack-integration"
	installID := requiredConfigString(&diags, m.InstallID, "config.install_id", channelType)
	channelID := requiredConfigString(&diags, m.ChannelID, "config.channel_id", channelType)
	diags.Append(disallowConfigFields(
		m, channelType, "install_id", "channel_id", "include_agent_prompt",
	)...)
	if diags.HasError() {
		return nil, diags
	}
	cfg := &logclient.SlackIntegrationConfig{
		ChannelConfigBase: logclient.ChannelConfigBase{Type: channelType},
		InstallID:         &installID,
		ChannelID:         &channelID,
	}
	if !m.IncludeAgentPrompt.IsUnknown() {
		cfg.IncludeAgentPrompt = m.IncludeAgentPrompt.ValueBoolPointer()
	}
	return cfg, nil
}

func requiredConfigString(diags *diag.Diagnostics, val types.String, fieldName, channelType string) string {
	if val.IsNull() {
		diags.AddError(
			"Invalid channel config",
			fmt.Sprintf("%s must be set for %s channels.", fieldName, channelType),
		)
		return ""
	}
	v := strings.TrimSpace(val.ValueString())
	if v == "" {
		diags.AddError(
			"Invalid channel config",
			fmt.Sprintf("%s cannot be empty for %s channels.", fieldName, channelType),
		)
	}
	return v
}

func disallowConfigString(val types.String, fieldName, channelType string) diag.Diagnostics {
	if strings.TrimSpace(val.ValueString()) == "" {
		return nil
	}
	return diag.Diagnostics{diag.NewErrorDiagnostic(
		"Invalid channel config",
		fmt.Sprintf("%s cannot be set for %s channels.", fieldName, channelType),
	)}
}

// disallowConfigFields rejects every config field of the channel type except
// the allowed ones, named without the "config." prefix.
func disallowConfigFields(m *ChannelConfigModel, channelType string, allowed ...string) diag.Diagnostics {
	var diags diag.Diagnostics
	for _, field := range []struct {
		name  string
		value types.String
	}{
		{"format", m.Format},
		{"url", m.URL},
		{"auth_key", m.AuthKey},
		{"routing_key", m.RoutingKey},
		{"region", m.Region},
		{"install_id", m.InstallID},
		{"channel_id", m.ChannelID},
		{"service_id", m.ServiceID},
	} {
		if !slices.Contains(allowed, field.name) {
			diags.Append(disallowConfigString(field.value, "config."+field.name, channelType)...)
		}
	}
	if !m.IncludeAgentPrompt.IsNull() && !m.IncludeAgentPrompt.IsUnknown() &&
		!slices.Contains(allowed, "include_agent_prompt") {
		diags.AddError(
			"Invalid channel config",
			fmt.Sprintf("config.include_agent_prompt cannot be set for %s channels.", channelType),
		)
	}
	return diags
}

func channelReadToModel(c *logclient.ChannelRead, m *ChannelModel) diag.Diagnostics {
	cfg := c.Config
	if _, ok := channelConfigModelToAPIMappers[cfg.Type]; !ok {
		return diag.Diagnostics{diag.NewErrorDiagnostic(
			"Invalid channel config", fmt.Sprintf("unsupported config type %q", cfg.Type),
		)}
	}

	m.ID = types.StringValue(c.ID)
	m.Name = types.StringValue(c.Label)
	m.Active = types.BoolValue(c.Active)
	m.Config = &ChannelConfigModel{
		Type:               types.StringValue(cfg.Type),
		Format:             types.StringPointerValue(cfg.Format),
		URL:                types.StringPointerValue(cfg.URL),
		AuthKey:            types.StringPointerValue(cfg.AuthKey),
		BaseURL:            types.StringPointerValue(cfg.BaseURL),
		RoutingKey:         types.StringPointerValue(cfg.RoutingKey),
		Region:             types.StringPointerValue(cfg.Region),
		InstallID:          types.StringPointerValue(cfg.InstallID),
		ChannelID:          types.StringPointerValue(cfg.ChannelID),
		ServiceID:          types.StringPointerValue(cfg.ServiceID),
		IncludeAgentPrompt: types.BoolPointerValue(cfg.IncludeAgentPrompt),
	}
	for _, r := range cfg.Recipients {
		m.Config.Recipients = append(m.Config.Recipients, types.StringValue(r))
	}
	return nil
}

func reconcileChannelConfigMaskedSecrets(remote, fallback *ChannelConfigModel) *ChannelConfigModel {
	if remote == nil || fallback == nil || !remote.Type.Equal(fallback.Type) {
		return remote
	}

	switch remote.Type.ValueString() {
	case "webhook":
		remote.URL = preserveMaskedConfigValue(remote.URL, fallback.URL, isMaskedWebhookURL)
	case "opsgenie":
		remote.AuthKey = preserveMaskedConfigValue(remote.AuthKey, fallback.AuthKey, isMaskedSecret)
	case "pagerduty":
		remote.RoutingKey = preserveMaskedConfigValue(
			remote.RoutingKey, fallback.RoutingKey, isMaskedSecret,
		)
	case "email":
		// Swap each stored address for the configured spelling Logfire stored it from.
		// Exact matches go first, and each configured address replaces one stored address
		// at most, so two configured spellings that differ only in case stay distinct.
		stored, configured := remote.Recipients, fallback.Recipients
		used := make([]bool, len(configured))
		matched := make([]bool, len(stored))
		for i, s := range stored {
			j := slices.IndexFunc(configured, func(c types.String) bool { return c.Equal(s) })
			if j >= 0 && !used[j] {
				used[j], matched[i] = true, true
			}
		}
		for i, s := range stored {
			for j, c := range configured {
				// Logfire returns addresses already normalized, so any other difference,
				// such as the case of a mailbox name, is a different recipient.
				if !matched[i] && !used[j] && canonicalEmail(c.ValueString()) == s.ValueString() {
					stored[i], used[j], matched[i] = c, true, true
				}
			}
		}
	}

	return remote
}

// caseInsensitiveMailboxes are the RFC 2142 mailbox names that Logfire lowercases.
var caseInsensitiveMailboxes = []string{
	"info", "marketing", "sales", "support", "abuse", "noc", "security", "postmaster",
	"hostmaster", "usenet", "news", "webmaster", "www", "uucp", "ftp",
}

// canonicalEmail applies Logfire's normalization, which uses email-validator.
// It trims the address and normalizes it to NFC. It lowercases a mailbox name
// from RFC 2142 that was ASCII before NFC. It maps the domain with UTS 46,
// which lowercases it and maps dots such as U+3002, and writes it in Unicode.
func canonicalEmail(address string) string {
	address = strings.TrimSpace(address)
	// Only a mailbox name that was ASCII before NFC is lowercased.
	ascii := strings.IndexFunc(
		address[:max(strings.LastIndex(address, "@"), 0)],
		func(r rune) bool { return r >= utf8.RuneSelf },
	) < 0
	address = norm.NFC.String(address)
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return address
	}
	local := address[:at]
	if lower := strings.ToLower(local); ascii && slices.Contains(caseInsensitiveMailboxes, lower) {
		local = lower
	}
	domain, err := idna.Lookup.ToUnicode(address[at+1:])
	if err != nil {
		domain = strings.ToLower(address[at+1:])
	}
	return local + "@" + domain
}

func preserveMaskedConfigValue(remote, fallback types.String, isMasked func(string) bool) types.String {
	if fallback.IsNull() {
		return remote
	}
	if remote.IsNull() {
		return fallback
	}
	if v := strings.TrimSpace(remote.ValueString()); v == "" || isMasked(v) {
		return fallback
	}
	return remote
}

func isMaskedWebhookURL(v string) bool {
	parsed, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return false
	}

	return parsed.Scheme != "" &&
		parsed.Host != "" &&
		parsed.Path == "/"+maskedChannelSecretPlaceholder &&
		parsed.RawQuery == "" &&
		parsed.Fragment == ""
}

func isMaskedSecret(v string) bool {
	v = strings.TrimSpace(v)
	return v == maskedChannelSecretPlaceholder ||
		(strings.HasPrefix(v, maskedChannelSecretPlaceholder) &&
			len(v) == len(maskedChannelSecretPlaceholder)+4)
}

func (r *ChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg, diags := channelConfigModelToAPI(plan.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := r.client.CreateChannel(ctx, logclient.ChannelCreate{
		Label:  plan.Name.ValueString(),
		Config: cfg,
	})
	if err != nil {
		resp.Diagnostics.AddError("Create channel failed", err.Error())
		return
	}

	var state ChannelModel
	if diags := channelReadToModel(out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	state.Config = reconcileChannelConfigMaskedSecrets(state.Config, plan.Config)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if desired := plan.Active.ValueBool(); desired != out.Active {
		payload := logclient.ChannelUpdate{Active: logclient.NullableFieldValue(desired)}
		updated, err := r.client.UpdateChannel(ctx, out.ID, payload)
		if err != nil {
			resp.Diagnostics.AddError("Create channel failed", fmt.Sprintf(
				"setting active flag: %v. The created channel %q is saved in state and "+
					"Terraform will mark it tainted. After fixing the error and verifying the "+
					"saved channel, untaint this resource before applying again to update it "+
					"without replacement.", err, state.ID.ValueString(),
			))
			return
		}
		if diags := channelReadToModel(updated, &state); diags.HasError() {
			resp.Diagnostics.Append(diags...)
			return
		}
		state.Config = reconcileChannelConfigMaskedSecrets(state.Config, plan.Config)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}

	tflog.Trace(ctx, "created channel", map[string]any{"id": state.ID.ValueString()})
}

func (r *ChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	priorConfig := state.Config

	out, status, err := r.client.GetChannel(ctx, state.ID.ValueString())
	if err != nil {
		if status == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read channel failed", err.Error())
		return
	}

	if diags := channelReadToModel(out, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	state.Config = reconcileChannelConfigMaskedSecrets(state.Config, priorConfig)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	payload := logclient.ChannelUpdate{
		Label: logclient.NullableFieldValue(plan.Name.ValueString()),
	}
	if v := plan.Active.ValueBool(); v != state.Active.ValueBool() {
		payload.Active = logclient.NullableFieldValue(v)
	}

	config := *plan.Config
	if config.Type.Equal(state.Config.Type) {
		if config.IncludeAgentPrompt.IsUnknown() {
			config.IncludeAgentPrompt = state.Config.IncludeAgentPrompt
		}
		if config.BaseURL.IsUnknown() {
			config.BaseURL = state.Config.BaseURL
		}
	}
	cfg, diags := channelConfigModelToAPI(&config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Logfire keeps a config the update leaves out. Resending an unchanged email config
	// fails after the last recipient unsubscribes, because Logfire keeps that address.
	current, _ := channelConfigModelToAPI(state.Config)
	if !reflect.DeepEqual(cfg, current) {
		payload.Config = &cfg
	}

	out, err := r.client.UpdateChannel(ctx, state.ID.ValueString(), payload)
	if err != nil {
		resp.Diagnostics.AddError("Update channel failed", err.Error())
		return
	}

	var newState ChannelModel
	if diags := channelReadToModel(out, &newState); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	newState.Config = reconcileChannelConfigMaskedSecrets(newState.Config, plan.Config)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *ChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteChannel(ctx, state.ID.ValueString())
	if err != nil && !logclient.IsNotFoundError(err) {
		resp.Diagnostics.AddError("Delete channel failed", err.Error())
	}
}

// ImportState imports a channel by its UUID, or by its label. Both forms
// resolve through the organization's channel list: an ID match is precise,
// and a label match covers the name form (including a label that happens to
// be UUID-shaped, which must not be mistaken for an ID).
func (r *ChannelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	rawID := strings.TrimSpace(req.ID)
	if rawID == "" {
		resp.Diagnostics.AddError(
			"Missing import ID",
			`Expected a non-empty ID. Use either the channel UUID or the channel name (label). `+
				`Example: terraform import logfire_channel.prod "alerts-webhook"`,
		)
		return
	}

	channels, err := r.client.ListChannels(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Import channel failed", err.Error())
		return
	}
	i := slices.IndexFunc(channels, func(c logclient.ChannelRead) bool {
		return c.ID == rawID
	})
	if i < 0 {
		i = slices.IndexFunc(channels, func(c logclient.ChannelRead) bool {
			return c.Label == rawID
		})
	}
	if i >= 0 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), channels[i].ID)...)
		return
	}
	resp.Diagnostics.AddError(
		"Import channel failed",
		fmt.Sprintf(
			"Channel %q not found in the credential's organization by ID or name. "+
				"List channel IDs with the Logfire API (GET /api/v1/channels/).", rawID,
		),
	)
}
