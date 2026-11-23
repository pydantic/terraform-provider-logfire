// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// keyedConfig configures the provider with a key and an optional organization.
func keyedConfig(baseURL, key, organization, body string) string {
	organizationLine := ""
	if organization != "" {
		organizationLine = fmt.Sprintf("organization = %q\n", organization)
	}
	return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = %q\n%s}\n%s", baseURL, key, organizationLine, body)
}

const limitsBody = `resource "logfire_gateway_spending_limits" "this" {
  daily_usd = 10
}`

// A resource created with a key of its own organization has no organization in
// its state, and the limits have one fixed ID. When the provider moves to an
// instance-admin key for another organization, the limits of that organization
// must stay alone: a refresh would read them, an apply would overwrite them,
// and a destroy would clear them.
func TestLifecycleKeySwitchToAnotherOrganizationLeavesItAlone(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	api.spendingLimits["org-team-b"] = map[string]any{"daily_usd": 99, "weekly_usd": nil, "monthly_usd": nil}
	ownKey := keyedConfig(server.URL, "team-a-key", "", limitsBody)
	retargeted := keyedConfig(server.URL, "test-token", "team-b", limitsBody)
	refused := regexp.MustCompile(`(?s)organization\s+"team-a".*"team-b"`)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ownKey, Check: resource.TestCheckResourceAttr("logfire_gateway_spending_limits.this", "daily_usd", "10")},
			{Config: retargeted, ExpectError: refused},
			{Config: retargeted, Destroy: true, ExpectError: refused},
			{Config: ownKey, ConfigPlanChecks: emptyPlan},
		},
	})

	if got := api.dailyLimitOf("team-b"); fmt.Sprint(got) != "99" {
		t.Fatalf("team-b's daily limit is %v, want its own 99", got)
	}
	if got := api.dailyLimitOf("team-a"); got != nil {
		t.Fatalf("team-a's daily limit is %v after destroy, want none", got)
	}
}

// The same key switch for the organization the resource is in keeps working:
// the instance-admin key reaches team-a through the exchange.
func TestLifecycleKeySwitchToTheSameOrganizationKeepsTheResource(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	ownKey := keyedConfig(server.URL, "team-a-key", "", limitsBody)
	adminKey := keyedConfig(server.URL, "test-token", "team-a", limitsBody)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ownKey},
			{Config: adminKey, ConfigPlanChecks: emptyPlan},
		},
	})
	if got := api.dailyLimitOf("team-a"); got != nil {
		t.Fatalf("team-a's daily limit is %v after destroy, want none", got)
	}
}

// Resources created through the exchange carry a record. A later key of the
// organization itself cannot list the instance organizations, so it proves it
// belongs to the recorded organization by reading its own.
func TestLifecycleExchangeRecordedResourcesMoveToTheOrganizationsOwnKey(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	body := `resource "logfire_project" "svc" {
  name = "svc"
}

resource "logfire_write_token" "svc" {
  project_id = logfire_project.svc.id
}

` + limitsBody
	exchanged := keyedConfig(server.URL, "test-token", "team-a", body)
	ownKey := keyedConfig(server.URL, "team-a-key", "", body)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: exchanged},
			{Config: ownKey, ConfigPlanChecks: emptyPlan},
		},
	})

	api.Lock()
	defer api.Unlock()
	if api.creates != 1 || api.deletes != 2 {
		t.Fatalf("creates=%d, deletes=%d, want the project and token kept, then destroyed", api.creates, api.deletes)
	}
}

// A key of another organization is refused the same way when it cannot say
// which organization it belongs to.
func TestLifecycleOwnKeyThatCannotReadItsOrganizationIsRefusedForRecordedResources(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	api.cannotReadOrganization = map[string]bool{"Bearer team-a-key": true}
	exchanged := keyedConfig(server.URL, "test-token", "team-a", limitsBody)
	ownKey := keyedConfig(server.URL, "team-a-key", "", limitsBody)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: exchanged},
			{Config: ownKey, ExpectError: regexp.MustCompile(`(?s)cannot\s+confirm.*organization:read`)},
			{Config: exchanged, ConfigPlanChecks: emptyPlan},
		},
	})
}

// The direct request for a project in another organization fails, and the
// exchange that would find it then fails without refusing the key. The project
// still exists, so refresh must report the failure and keep it.
func TestLifecycleProjectSurvivesAnAmbiguousExchangeFailure(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	for name, status := range map[string]int{"not found": 404, "forbidden": 403} {
		t.Run(name, func(t *testing.T) {
			server, api := newProjectOrganizationAPI(t)
			api.cannotReadOrganization = map[string]bool{"Bearer team-a-key": true}
			ownKey := keyedConfig(server.URL, "team-a-key", "", `resource "logfire_project" "svc" {
  name = "svc"
}`)
			adminKey := keyedConfig(server.URL, "test-token", "", `resource "logfire_project" "svc" {
  name = "svc"
}`)
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: ownKey},
					{
						PreConfig: func() {
							api.Lock()
							api.otherOrganizationProjectStatus = status
							api.exchangeFailure = `{"error":"temporarily_unavailable","error_description":"try again"}`
							api.Unlock()
						},
						Config:      adminKey,
						ExpectError: regexp.MustCompile(`temporarily_unavailable|API error \(status 403\)`),
					},
					{
						PreConfig: func() {
							api.Lock()
							api.exchangeFailure = ""
							api.Unlock()
						},
						Config: adminKey, ConfigPlanChecks: emptyPlan,
					},
				},
			})
			api.Lock()
			defer api.Unlock()
			if api.creates != 1 {
				t.Fatalf("creates = %d, want the project kept through the failed exchange", api.creates)
			}
		})
	}
}

// An organization that is not known until apply cannot be checked at plan
// time. A replacement would delete the project before a create in a missing
// organization fails.
func TestLifecycleUnknownOrganizationDoesNotReplaceTheProject(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	config := func(target string) string {
		return keyedConfig(server.URL, "test-token", "", fmt.Sprintf(`resource "terraform_data" "target" {
  input = %q
}

resource "logfire_project" "svc" {
  organization = terraform_data.target.output
  name         = "svc"
}`, target))
	}

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("team-b")},
			{Config: config("team-c"), ExpectError: regexp.MustCompile(`Organization not known until apply`)},
			{Config: config("team-b"), ConfigPlanChecks: emptyPlan},
		},
	})

	api.Lock()
	defer api.Unlock()
	if api.creates != 1 || api.deletes != 1 {
		t.Fatalf("creates=%d, deletes=%d, want the project kept through the unknown organization, then destroyed", api.creates, api.deletes)
	}
}

// A project that sets no organization stays where its state says. When the
// provider organization changes, editing the project must not change it in the
// old organization from a provider that now points elsewhere.
func TestLifecycleProjectWithoutOrganizationRefusesARetargetedProvider(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	config := func(organization, description string) string {
		return keyedConfig(server.URL, "test-token", organization, fmt.Sprintf(`resource "logfire_project" "svc" {
  name        = "svc"
  description = %q
}`, description))
	}

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("team-a", "first")},
			{Config: config("team-b", "second"), ExpectError: regexp.MustCompile(`(?s)lives\s+in\s+organization\s+"team-a".*sets\s+no\s+organization`)},
			{Config: config("team-a", "first"), ConfigPlanChecks: emptyPlan},
		},
	})

	api.Lock()
	defer api.Unlock()
	if api.creates != 1 {
		t.Fatalf("creates = %d, want the project kept", api.creates)
	}
}

// The organization record is saved before the project is created. A failure to
// read the organization then stops the create instead of leaving a project that
// Terraform marks as tainted and replaces on the next apply.
func TestLifecycleProjectCreateFailsBeforeCreatingWhenTheOrganizationCannotBeRecorded(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	config := projectConfig(server.URL, "team-a", `name = "svc"`)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					api.Lock()
					api.organizationListStatus = 403
					api.Unlock()
				},
				Config:      config,
				ExpectError: regexp.MustCompile(`Organization lookup failed`),
			},
			{
				PreConfig: func() {
					api.Lock()
					api.organizationListStatus = 0
					api.Unlock()
				},
				Config: config,
			},
		},
	})

	api.Lock()
	defer api.Unlock()
	if api.creates != 1 || api.deletes != 1 {
		t.Fatalf("creates=%d, deletes=%d, want one project, created once and destroyed once", api.creates, api.deletes)
	}
}
