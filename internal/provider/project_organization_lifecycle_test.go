// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// assertActedOnlyIn fails when a project request reached an organization other
// than the one expected. The organization list lookup of an import is allowed
// to start in the credential's own organization.
func (api *projectOrganizationAPI) assertActedOnlyIn(t *testing.T, organization string, allowedLists int) {
	t.Helper()
	api.Lock()
	defer api.Unlock()
	lists := 0
	for _, request := range api.requests {
		if request.organization == organization {
			continue
		}
		if request.method == http.MethodGet && request.path == projectsCollection && lists < allowedLists {
			lists++
			continue
		}
		t.Errorf("%s %s reached organization %q, want %q", request.method, request.path, request.organization, organization)
	}
	if api.forbidden != 0 {
		t.Errorf("%d requests asked an organization for a project in another one", api.forbidden)
	}
}

func projectConfig(baseURL, providerOrganization, resourceBody string) string {
	organization := ""
	if providerOrganization != "" {
		organization = fmt.Sprintf("organization = %q\n", providerOrganization)
	}
	return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\n%s}\nresource \"logfire_project\" \"svc\" {\n%s\n}",
		baseURL, organization, resourceBody)
}

func TestLifecycleProjectOrganizationContext(t *testing.T) {
	// The cases run one after another so that t.Setenv can keep a developer's
	// LOGFIRE_ORGANIZATION out of the "not set" case.
	t.Setenv("LOGFIRE_ORGANIZATION", "")
	const address = "logfire_project.svc"

	t.Run("resource organization", func(t *testing.T) {
		server, api := newProjectOrganizationAPI(t)
		api.rawKeyOnlyInstanceAdmin = true
		config := func(description string) string {
			return projectConfig(server.URL, "", fmt.Sprintf("organization = \"team-a\"\nname = \"svc\"\ndescription = %q", description))
		}
		noPlanChange := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: config("first"),
					Check: resource.ComposeTestCheckFunc(
						resource.TestCheckResourceAttr(address, "organization", "team-a"),
						resource.TestCheckResourceAttr(address, "id", "project-1")),
				},
				// The refresh before this plan reaches the project only through the exchange.
				{Config: config("first"), ConfigPlanChecks: noPlanChange},
				{
					Config:           config("second"),
					ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate)}},
					Check:            resource.TestCheckResourceAttr(address, "description", "second"),
				},
				{
					Config: fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\n}\nremoved {\nfrom = %s\nlifecycle { destroy = false }\n}", server.URL, address),
				},
				{Config: config("second"), ResourceName: address, ImportState: true, ImportStatePersist: true, ImportStateId: "team-a/svc"},
				{Config: config("second"), ConfigPlanChecks: noPlanChange},
			},
		})

		api.Lock()
		creates, deletes := api.creates, api.deletes
		exchanges := append([]url.Values(nil), api.exchanges...)
		api.Unlock()
		if creates != 1 || deletes != 1 {
			t.Fatalf("creates=%d, deletes=%d, want one of each", creates, deletes)
		}
		if len(exchanges) == 0 {
			t.Fatal("the provider never exchanged the key")
		}
		for _, form := range exchanges {
			if form.Get("subject_token") != "test-token" || form.Get("audience") != server.URL+"/team-a" ||
				!strings.Contains(" "+form.Get("scope")+" ", " organization:create_project ") {
				t.Fatalf("exchange form = %v, want the raw key for team-a with the project scopes", form)
			}
		}
		// The import looks in the credential's own organization first.
		api.assertActedOnlyIn(t, "team-a", 1)
	})

	t.Run("provider organization", func(t *testing.T) {
		server, api := newProjectOrganizationAPI(t)
		config := projectConfig(server.URL, "team-b", "name = \"svc\"")
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config, Check: resource.TestCheckResourceAttr(address, "organization", "team-b")},
				{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			},
		})
		api.assertActedOnlyIn(t, "team-b", 0)
		api.Lock()
		defer api.Unlock()
		if api.creates != 1 || api.deletes != 1 {
			t.Fatalf("creates=%d, deletes=%d, want one of each", api.creates, api.deletes)
		}
	})

	t.Run("resource organization overrides the provider", func(t *testing.T) {
		server, api := newProjectOrganizationAPI(t)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: projectConfig(server.URL, "team-b", "organization = \"team-a\"\nname = \"svc\""),
				Check:  resource.TestCheckResourceAttr(address, "organization", "team-a"),
			}},
		})
		api.assertActedOnlyIn(t, "team-a", 0)
	})

	t.Run("no organization", func(t *testing.T) {
		server, api := newProjectOrganizationAPI(t)
		config := projectConfig(server.URL, "", "name = \"svc\"")
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config, Check: resource.TestCheckResourceAttr(address, "organization", "admin")},
				{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			},
		})
		api.assertActedOnlyIn(t, "admin", 0)
		api.Lock()
		defer api.Unlock()
		if len(api.exchanges) != 0 {
			t.Fatalf("a provider without an organization made %d token exchanges", len(api.exchanges))
		}
		if api.creates != 1 || api.deletes != 1 {
			t.Fatalf("creates=%d, deletes=%d, want one of each", api.creates, api.deletes)
		}
	})

	t.Run("changing the organization replaces the project", func(t *testing.T) {
		server, api := newProjectOrganizationAPI(t)
		config := func(organization string) string {
			return projectConfig(server.URL, "", fmt.Sprintf("organization = %q\nname = \"svc\"", organization))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("team-a")},
				{
					Config:           config("team-b"),
					ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionDestroyBeforeCreate)}},
					Check:            resource.TestCheckResourceAttr(address, "organization", "team-b"),
				},
			},
		})
		api.Lock()
		defer api.Unlock()
		if api.creates != 2 || api.deletes != 2 || api.forbidden != 0 {
			t.Fatalf("creates=%d, deletes=%d, forbidden=%d, want two creates, two deletes, no forbidden requests", api.creates, api.deletes, api.forbidden)
		}
		for organization, projects := range api.projects {
			if len(projects) != 0 {
				t.Fatalf("organization %q still holds %d projects after destroy", organization, len(projects))
			}
		}
	})

	t.Run("an organization with a path is invalid", func(t *testing.T) {
		server, _ := newProjectOrganizationAPI(t)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      projectConfig(server.URL, "", "organization = \"team-a/\"\nname = \"svc\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("lowercase letters"),
			}},
		})
	})

	t.Run("organization removed outside Terraform", func(t *testing.T) {
		server, api := newProjectOrganizationAPI(t)
		config := projectConfig(server.URL, "", "organization = \"team-a\"\nname = \"svc\"")
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config},
				{
					PreConfig: func() { api.remove("team-a") },
					// The refresh drops the project from state, so the plan creates it again.
					Config:   config,
					PlanOnly: true, ExpectNonEmptyPlan: true,
				},
			},
		})
	})
}
