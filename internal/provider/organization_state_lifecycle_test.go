// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var emptyPlan = resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}

// spendingLimitsConfig manages the Gateway spending limits of the organization
// that the provider names.
func spendingLimitsConfig(baseURL, organization string) string {
	return fmt.Sprintf(`provider "logfire" {
  base_url     = %q
  api_key      = "test-token"
  organization = %q
}

resource "logfire_gateway_spending_limits" "this" {
  daily_usd = 10
}`, baseURL, organization)
}

// A provider whose organization changes must not act on the new
// organization's resources with the old organization's state. The limits are
// one per organization, so a refresh in team-b would read team-b's limits and
// a destroy would clear them.
func TestLifecycleRetargetedProviderLeavesOtherOrganizationAlone(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	api.spendingLimits["org-team-b"] = map[string]any{"daily_usd": 99, "weekly_usd": nil, "monthly_usd": nil}
	retargeted := regexp.MustCompile(`organization "team-a".*\n?.*"team-b"`)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: spendingLimitsConfig(server.URL, "team-a"),
				Check:  resource.TestCheckResourceAttr("logfire_gateway_spending_limits.this", "daily_usd", "10"),
			},
			{Config: spendingLimitsConfig(server.URL, "team-b"), ExpectError: retargeted},
			{Config: spendingLimitsConfig(server.URL, "team-b"), Destroy: true, ExpectError: retargeted},
			{Config: spendingLimitsConfig(server.URL, "team-a"), ConfigPlanChecks: emptyPlan},
		},
	})

	if got := api.dailyLimitOf("team-b"); fmt.Sprint(got) != "99" {
		t.Fatalf("team-b's daily limit is %v, want its own 99", got)
	}
	if got := api.dailyLimitOf("team-a"); got != nil {
		t.Fatalf("team-a's daily limit is %v after destroy, want none", got)
	}
}

// An organization renamed outside Terraform keeps its ID. A resource that acts
// in the provider organization follows it once the provider names the new
// name, and refuses the old name, which no longer reaches it.
func TestLifecycleProviderFollowsRenamedOrganization(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: spendingLimitsConfig(server.URL, "team-a")},
			{
				PreConfig:   func() { api.rename("team-a", "team-z") },
				Config:      spendingLimitsConfig(server.URL, "team-a"),
				ExpectError: regexp.MustCompile(`renamed to "team-z"`),
			},
			{Config: spendingLimitsConfig(server.URL, "team-z"), ConfigPlanChecks: emptyPlan},
		},
	})

	if got := api.dailyLimitOf("team-z"); got != nil {
		t.Fatalf("the renamed organization's daily limit is %v after destroy, want none", got)
	}
}

// A project whose organization was renamed outside Terraform still exists.
// The refresh follows the organization to its new name instead of dropping
// the project. A configuration that keeps the old name must not replace the
// project, because the create in the missing organization would fail after
// the delete. The destroy deletes the project in the renamed organization.
func TestLifecycleProjectOfRenamedOrganization(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	config := func(organization string) string {
		return projectConfig(server.URL, "", fmt.Sprintf("organization = %q\nname = \"svc\"", organization))
	}

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("team-a")},
			{
				PreConfig:   func() { api.rename("team-a", "team-z") },
				Config:      config("team-a"),
				ExpectError: regexp.MustCompile(`(?s)"team-a" does not exist.*project is in "team-z"`),
			},
			{
				Config:           config("team-z"),
				ConfigPlanChecks: emptyPlan,
				Check:            resource.TestCheckResourceAttr("logfire_project.svc", "organization", "team-z"),
			},
		},
	})

	api.Lock()
	defer api.Unlock()
	if api.creates != 1 || api.deletes != 1 || len(api.projects["org-team-a"]) != 0 {
		t.Fatalf("creates=%d, deletes=%d, projects left=%d, want the one project kept, then destroyed",
			api.creates, api.deletes, len(api.projects["org-team-a"]))
	}
}

// After an organization is deleted outside Terraform, its resources are gone
// too. The refresh drops them, including the resources without an
// organization argument that act in the provider organization, and the
// destroy succeeds. The configuration has one provider block, because the
// test framework serves every provider block from one provider process and
// the last configured block would win.
func TestLifecycleResourcesOfDeletedOrganization(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	server, api := newProjectOrganizationAPI(t)
	config := projectConfig(server.URL, "team-a", `name = "svc"
}

resource "logfire_write_token" "svc" {
  project_id = logfire_project.svc.id
}

resource "logfire_gateway_spending_limits" "team" {
  daily_usd = 5`)

	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { api.remove("team-a") },
				Config:    config,
				PlanOnly:  true, ExpectNonEmptyPlan: true,
			},
			{Config: config, Destroy: true},
		},
		CheckDestroy: func(state *terraform.State) error {
			if resources := state.RootModule().Resources; len(resources) != 0 {
				return fmt.Errorf("state still holds %d resources", len(resources))
			}
			return nil
		},
	})
}

// A project created with a key of its own organization can move to the
// instance-admin key by naming its organization. The instance-admin key alone
// cannot reach the project, so the provider goes through the exchange.
func TestLifecycleProjectMovesToInstanceAdminKey(t *testing.T) {
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	for name, onlyInstanceAdmin := range map[string]bool{"key without project scopes": true, "key with project scopes": false} {
		t.Run(name, func(t *testing.T) {
			server, api := newProjectOrganizationAPI(t)
			ownKey := fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"team-a-key\"\n}\nresource \"logfire_project\" \"svc\" {\nname = \"svc\"\n}", server.URL)
			instanceAdmin := projectConfig(server.URL, "", "organization = \"team-a\"\nname = \"svc\"")
			updated := projectConfig(server.URL, "", "organization = \"team-a\"\nname = \"svc\"\ndescription = \"moved\"")

			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: ownKey, Check: resource.TestCheckResourceAttr("logfire_project.svc", "organization", "team-a")},
					{
						PreConfig: func() {
							api.Lock()
							api.rawKeyOnlyInstanceAdmin = onlyInstanceAdmin
							api.Unlock()
						},
						Config: instanceAdmin, ConfigPlanChecks: emptyPlan,
					},
					{
						Config:           updated,
						ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("logfire_project.svc", plancheck.ResourceActionUpdate)}},
						Check:            resource.TestCheckResourceAttr("logfire_project.svc", "description", "moved"),
					},
				},
			})

			api.Lock()
			defer api.Unlock()
			if api.creates != 1 || api.deletes != 1 {
				t.Fatalf("creates=%d, deletes=%d, want the one project kept, then destroyed", api.creates, api.deletes)
			}
		})
	}
}
