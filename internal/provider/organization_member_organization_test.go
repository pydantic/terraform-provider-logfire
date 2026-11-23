// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	cyID  = "0191a5c0-0000-7000-8000-0000000000c1"
	danID = "0191a5c0-0000-7000-8000-0000000000d4"
)

// seededMembers gives Alice a different role in each organization, so that a
// request in the wrong organization changes a role the test then checks.
func seededMembers() map[string][]logclient.OrganizationMember {
	return map[string][]logclient.OrganizationMember{
		"org-team-a": {
			{ID: aliceID, Name: "Alice", Email: "alice@example.com", Role: "member", MemberSince: "2026-01-01T00:00:00Z"},
			{ID: bobID, Name: "Bob", Email: "bob@example.com", Role: "guest", MemberSince: "2026-01-02T00:00:00Z"},
		},
		"org-team-b": {
			{ID: aliceID, Name: "Alice", Email: "alice@example.com", Role: "guest", MemberSince: "2026-02-01T00:00:00Z"},
			{ID: cyID, Name: "Cy", Email: "cy@example.com", Role: "member", MemberSince: "2026-02-02T00:00:00Z"},
		},
		"org-admin": {
			{ID: aliceID, Name: "Alice", Email: "alice@example.com", Role: "admin", MemberSince: "2026-03-01T00:00:00Z"},
			{ID: danID, Name: "Dan", Email: "dan@example.com", Role: "member", MemberSince: "2026-03-02T00:00:00Z"},
		},
	}
}

func (api *projectOrganizationAPI) serveMembers(w http.ResponseWriter, r *http.Request, organizationID string) {
	organization := api.organizationName(organizationID)
	if r.URL.Path == "/api/v1/members/" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(api.members[organizationID])
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/members/"), "/")
	index := slices.IndexFunc(api.members[organizationID], func(m logclient.OrganizationMember) bool { return m.ID == id })
	if index < 0 {
		api.fail(w, http.StatusNotFound, `{"detail":"Organization member not found"}`)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var body struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.fail(w, http.StatusBadRequest, `{"detail":"bad body"}`)
			return
		}
		api.members[organizationID][index].Role = body.Role
		api.memberWrites = append(api.memberWrites, fmt.Sprintf("%s PATCH %s=%s", organization, id, body.Role))
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		api.members[organizationID] = slices.Delete(api.members[organizationID], index, index+1)
		api.memberWrites = append(api.memberWrites, fmt.Sprintf("%s DELETE %s", organization, id))
		w.WriteHeader(http.StatusNoContent)
	default:
		api.fail(w, http.StatusMethodNotAllowed, `{"detail":"Method Not Allowed"}`)
	}
}

// memberRole is the role of a user in an organization, and whether the user is a member.
func (api *projectOrganizationAPI) memberRole(organization, id string) (string, bool) {
	api.Lock()
	defer api.Unlock()
	for _, m := range api.members[api.organizations[organization]] {
		if m.ID == id {
			return m.Role, true
		}
	}
	return "", false
}

func (api *projectOrganizationAPI) writes() []string {
	api.Lock()
	defer api.Unlock()
	return slices.Clone(api.memberWrites)
}

func (api *projectOrganizationAPI) assertWrites(t *testing.T, want ...string) {
	t.Helper()
	if got := api.writes(); !slices.Equal(got, want) {
		t.Errorf("member writes = %q, want %q", got, want)
	}
}

// assertMemberRequestsIn fails when a request reached an organization other than the expected one.
func (api *projectOrganizationAPI) assertMemberRequestsIn(t *testing.T, organization string) {
	t.Helper()
	api.Lock()
	defer api.Unlock()
	for _, request := range api.requests {
		if request.organization != organization {
			t.Errorf("%s %s reached organization %q, want %q", request.method, request.path, request.organization, organization)
		}
	}
}

func (api *projectOrganizationAPI) assertRole(t *testing.T, organization, id, want string) {
	t.Helper()
	if got, ok := api.memberRole(organization, id); !ok || got != want {
		t.Errorf("%s has role %q (member: %t) in %s, want %q", id, got, ok, organization, want)
	}
}

func memberOrganizationConfig(baseURL, apiKey, providerOrganization, resourceBody string) string {
	return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = %q\n%s}\nresource \"logfire_organization_member\" \"test\" {\n%s\n}",
		baseURL, apiKey, organizationArgument(providerOrganization), resourceBody)
}

func memberResourceBody(organization, userID, role string) string {
	if organization == "" {
		return fmt.Sprintf("user_id = %q\nrole = %q", userID, role)
	}
	return fmt.Sprintf("organization = %q\nuser_id = %q\nrole = %q", organization, userID, role)
}

func resourceAction(action plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(memberResource, action)}}
}

func TestLifecycleOrganizationMemberOrganizationContext(t *testing.T) {
	t.Parallel()

	t.Run("resource organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(role string) string {
			return memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a", aliceID, role))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: config("admin"),
					Check: resource.ComposeTestCheckFunc(
						resource.TestCheckResourceAttr(memberResource, "organization", "team-a"),
						resource.TestCheckResourceAttr(memberResource, "id", aliceID),
						resource.TestCheckResourceAttr(memberResource, "role", "admin"),
					),
				},
				{Config: config("admin"), ConfigPlanChecks: emptyPlan},
				{
					Config:           config("guest"),
					ConfigPlanChecks: resourceAction(plancheck.ResourceActionUpdate),
					Check:            resource.TestCheckResourceAttr(memberResource, "organization", "team-a"),
				},
				{
					Config: fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\norganization = \"\"\n}\nremoved {\nfrom = %s\nlifecycle { destroy = false }\n}", server.URL, memberResource),
				},
				{Config: config("guest"), ResourceName: memberResource, ImportState: true, ImportStatePersist: true, ImportStateId: "team-a/" + aliceID},
				{Config: config("guest"), ConfigPlanChecks: emptyPlan},
			},
		})

		api.assertMemberRequestsIn(t, "team-a")
		api.assertWrites(t, "team-a PATCH "+aliceID+"=admin", "team-a PATCH "+aliceID+"=guest", "team-a DELETE "+aliceID)
		api.assertRole(t, "team-b", aliceID, "guest")
		api.assertRole(t, "admin", aliceID, "admin")
		api.Lock()
		defer api.Unlock()
		if len(api.exchanges) == 0 {
			t.Fatal("the provider never exchanged the key")
		}
		for _, form := range api.exchanges {
			if form.Get("audience") != server.URL+"/team-a" || !strings.Contains(" "+form.Get("scope")+" ", " organization:write_member ") {
				t.Fatalf("exchange form = %v, want the audience team-a and the member scopes", form)
			}
		}
	})

	t.Run("provider organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := memberOrganizationConfig(server.URL, "test-token", "team-b", memberResourceBody("", cyID, "member"))
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config, Check: resource.TestCheckResourceAttr(memberResource, "organization", "team-b")},
				{Config: config, ConfigPlanChecks: emptyPlan},
				{Config: config, ResourceName: memberResource, ImportState: true, ImportStateId: cyID, ImportStateVerify: true},
			},
		})
		api.assertMemberRequestsIn(t, "team-b")
		api.assertWrites(t, "team-b DELETE "+cyID)
	})

	t.Run("resource organization overrides the provider", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: memberOrganizationConfig(server.URL, "test-token", "team-b", memberResourceBody("team-a", bobID, "guest")),
				Check:  resource.TestCheckResourceAttr(memberResource, "organization", "team-a"),
			}},
		})
		api.assertMemberRequestsIn(t, "team-a")
		api.assertWrites(t, "team-a DELETE "+bobID)
		api.assertRole(t, "team-b", cyID, "member")
	})

	t.Run("no organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(role string) string {
			return memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("", danID, role))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("member"), Check: resource.TestCheckResourceAttr(memberResource, "organization", "admin")},
				{Config: config("member"), ConfigPlanChecks: emptyPlan},
				{
					Config:           config("guest"),
					ConfigPlanChecks: resourceAction(plancheck.ResourceActionUpdate),
					Check:            resource.TestCheckResourceAttr(memberResource, "organization", "admin"),
				},
			},
		})
		api.assertMemberRequestsIn(t, "admin")
		api.assertWrites(t, "admin PATCH "+danID+"=guest", "admin DELETE "+danID)
		api.Lock()
		defer api.Unlock()
		if len(api.exchanges) != 0 {
			t.Fatalf("a provider without an organization made %d token exchanges", len(api.exchanges))
		}
	})

	t.Run("changing the organization replaces the member", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(organization string) string {
			return memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody(organization, aliceID, "admin"))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("team-a")},
				{
					Config:           config("team-b"),
					ConfigPlanChecks: resourceAction(plancheck.ResourceActionDestroyBeforeCreate),
					Check:            resource.TestCheckResourceAttr(memberResource, "organization", "team-b"),
				},
			},
		})
		api.assertWrites(t,
			"team-a PATCH "+aliceID+"=admin", "team-a DELETE "+aliceID,
			"team-b PATCH "+aliceID+"=admin", "team-b DELETE "+aliceID)
		api.assertRole(t, "admin", aliceID, "admin")
	})

	t.Run("replacement needs an existing organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(organization string) string {
			return memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody(organization, bobID, "guest"))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("team-a")},
				{Config: config("team-c"), ExpectError: regexp.MustCompile(`Organization "team-c" does not exist`)},
			},
		})
		api.assertWrites(t, "team-a DELETE "+bobID)
	})

	t.Run("an organization with a path is invalid", func(t *testing.T) {
		t.Parallel()
		server, _ := newProjectOrganizationAPI(t)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a/", aliceID, "guest")),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("lowercase letters"),
			}},
		})
	})

	t.Run("import names the organization that lacks the member", func(t *testing.T) {
		t.Parallel()
		server, _ := newProjectOrganizationAPI(t)
		config := memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-b", cyID, "member"))
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config, ResourceName: memberResource, ImportState: true, ImportStateId: "team-b/" + bobID, ExpectError: regexp.MustCompile("no member with the user ID")},
			},
		})
	})
}

// The organization of a member is recorded by ID, so a rename, a deletion, a
// provider that names another organization, and a credential switch each give
// their own answer instead of acting in whatever the name reaches now.
func TestLifecycleOrganizationMemberOrganizationState(t *testing.T) {
	t.Parallel()

	t.Run("renamed organization with a resource organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(organization string) string {
			return memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody(organization, aliceID, "admin"))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("team-a")},
				{
					PreConfig:   func() { api.rename("team-a", "team-z") },
					Config:      config("team-a"),
					ExpectError: regexp.MustCompile(`(?s)"team-a" does not exist.*member is in "team-z"`),
				},
				{
					Config:           config("team-z"),
					ConfigPlanChecks: emptyPlan,
					Check:            resource.TestCheckResourceAttr(memberResource, "organization", "team-z"),
				},
			},
		})
		api.assertWrites(t, "team-a PATCH "+aliceID+"=admin", "team-z DELETE "+aliceID)
		api.assertRole(t, "team-b", aliceID, "guest")
	})

	t.Run("renamed organization with a provider organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(organization string) string {
			return memberOrganizationConfig(server.URL, "test-token", organization, memberResourceBody("", aliceID, "admin"))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("team-b")},
				{
					PreConfig:   func() { api.rename("team-b", "team-y") },
					Config:      config("team-b"),
					ExpectError: regexp.MustCompile(`(?s)lives in organization\s+"team-y"`),
				},
				{Config: config("team-y"), ConfigPlanChecks: emptyPlan, Check: resource.TestCheckResourceAttr(memberResource, "organization", "team-y")},
			},
		})
		api.assertWrites(t, "team-b PATCH "+aliceID+"=admin", "team-y DELETE "+aliceID)
		api.assertRole(t, "team-a", aliceID, "member")
	})

	t.Run("deleted organization", func(t *testing.T) {
		t.Parallel()
		for name, config := range map[string]func(string) string{
			"resource organization": func(url string) string {
				return memberOrganizationConfig(url, "test-token", "", memberResourceBody("team-a", aliceID, "admin"))
			},
			"provider organization": func(url string) string {
				return memberOrganizationConfig(url, "test-token", "team-a", memberResourceBody("", aliceID, "admin"))
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				server, api := newProjectOrganizationAPI(t)
				resource.Test(t, resource.TestCase{
					IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
					Steps: []resource.TestStep{
						{Config: config(server.URL)},
						{
							PreConfig: func() { api.remove("team-a") },
							Config:    config(server.URL),
							PlanOnly:  true, ExpectNonEmptyPlan: true,
						},
						{Config: config(server.URL), Destroy: true},
					},
				})
				api.assertWrites(t, "team-a PATCH "+aliceID+"=admin")
				api.assertRole(t, "admin", aliceID, "admin")
				api.assertRole(t, "team-b", aliceID, "guest")
			})
		}
	})

	t.Run("provider organization switches", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(organization string) string {
			return memberOrganizationConfig(server.URL, "test-token", organization, memberResourceBody("", aliceID, "admin"))
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("team-a")},
				{Config: config("team-b"), ExpectError: regexp.MustCompile(`(?s)lives in organization\s+"team-a".*provider now acts in\s+"team-b"`)},
				{Config: fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\norganization = \"team-b\"\n}", server.URL)},
			},
		})
		api.assertWrites(t, "team-a PATCH "+aliceID+"=admin", "team-a DELETE "+aliceID)
		api.assertRole(t, "team-b", aliceID, "guest")
	})

	t.Run("credential switches", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		own := func(body string) string { return memberOrganizationConfig(server.URL, "team-a-key", "", body) }
		body := memberResourceBody("", aliceID, "admin")
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: own(body), Check: resource.TestCheckResourceAttr(memberResource, "organization", "team-a")},
				{
					Config:      memberOrganizationConfig(server.URL, "test-token", "team-b", body),
					ExpectError: regexp.MustCompile(`(?s)lives in organization\s+"team-a",\s+but it sets no\s+organization.*acts in\s+"team-b"`),
				},
				{
					Config:           memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a", aliceID, "billing")),
					ConfigPlanChecks: resourceAction(plancheck.ResourceActionUpdate),
					Check:            resource.TestCheckResourceAttr(memberResource, "organization", "team-a"),
				},
				{Config: own(memberResourceBody("", aliceID, "billing")), ConfigPlanChecks: emptyPlan},
			},
		})
		api.assertWrites(t, "team-a PATCH "+aliceID+"=admin", "team-a PATCH "+aliceID+"=billing", "team-a DELETE "+aliceID)
		api.assertRole(t, "team-b", aliceID, "guest")
		api.assertRole(t, "admin", aliceID, "admin")
	})

	t.Run("a key of another organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		body := memberResourceBody("", aliceID, "admin")
		viaProvider := memberOrganizationConfig(server.URL, "test-token", "team-b", body)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: viaProvider},
				{
					Config:      memberOrganizationConfig(server.URL, "team-a-key", "", body),
					ExpectError: regexp.MustCompile(`Cannot confirm the organization of this resource`),
				},
				{Config: viaProvider, ConfigPlanChecks: emptyPlan},
			},
		})
		api.assertWrites(t, "team-b PATCH "+aliceID+"=admin", "team-b DELETE "+aliceID)
		api.assertRole(t, "team-a", aliceID, "member")
	})
}

func TestLifecycleOrganizationMembersDataSourceOrganization(t *testing.T) {
	t.Parallel()
	const name = "data.logfire_organization_members.all"
	for testName, tc := range map[string]struct {
		providerOrganization, organization, wantOrganization string
		wantEmails                                           []string
	}{
		"provider organization":      {providerOrganization: "team-b", wantOrganization: "team-b", wantEmails: []string{"alice@example.com", "cy@example.com"}},
		"argument over the provider": {providerOrganization: "team-b", organization: "team-a", wantOrganization: "team-a", wantEmails: []string{"alice@example.com", "bob@example.com"}},
		"no organization":            {wantOrganization: "admin", wantEmails: []string{"alice@example.com", "dan@example.com"}},
	} {
		t.Run(testName, func(t *testing.T) {
			t.Parallel()
			server, api := newProjectOrganizationAPI(t)
			argument := ""
			if tc.organization != "" {
				argument = fmt.Sprintf("organization = %q", tc.organization)
			}
			config := fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\n%s}\ndata \"logfire_organization_members\" \"all\" {\n%s\n}",
				server.URL, organizationArgument(tc.providerOrganization), argument)
			checks := []resource.TestCheckFunc{resource.TestCheckResourceAttr(name, "members.#", fmt.Sprint(len(tc.wantEmails)))}
			for i, email := range tc.wantEmails {
				checks = append(checks, resource.TestCheckResourceAttr(name, fmt.Sprintf("members.%d.email", i), email))
			}
			checks = append(checks, resource.TestCheckResourceAttr(name, "organization", tc.wantOrganization))
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{Config: config, Check: resource.ComposeTestCheckFunc(checks...)}},
			})
			api.assertWrites(t)
			api.assertMemberRequestsIn(t, tc.wantOrganization)
		})
	}

	t.Run("a key that cannot read its organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		api.cannotReadOrganization = map[string]bool{"Bearer test-token": true}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\norganization = \"\"\n}\ndata \"logfire_organization_members\" \"all\" {}", server.URL),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "members.#", "2"),
					resource.TestCheckNoResourceAttr(name, "organization")),
			}},
		})
	})

	t.Run("an organization with a path is invalid", func(t *testing.T) {
		t.Parallel()
		server, _ := newProjectOrganizationAPI(t)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:   fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\norganization = \"\"\n}\ndata \"logfire_organization_members\" \"all\" {\norganization = \"team-a/\"\n}", server.URL),
				PlanOnly: true, ExpectError: regexp.MustCompile("lowercase letters"),
			}},
		})
	})
}

// An empty provider organization turns LOGFIRE_ORGANIZATION off, so these tests can run in parallel.
func organizationArgument(organization string) string {
	if organization == "" {
		return "organization = \"\"\n"
	}
	return fmt.Sprintf("organization = %q\n", organization)
}

// beforeApply runs f after Terraform has planned and before it applies, so that a test can change the
// instance in the gap that a refresh cannot close.
type beforeApply struct{ f func() }

func (b beforeApply) CheckPlan(context.Context, plancheck.CheckPlanRequest, *plancheck.CheckPlanResponse) {
	b.f()
}

func planThen(f func(), checks ...plancheck.PlanCheck) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: append(checks, beforeApply{f})}
}

func (api *projectOrganizationAPI) removeMember(organization, id string) {
	api.Lock()
	defer api.Unlock()
	key := api.organizations[organization]
	api.members[key] = slices.DeleteFunc(api.members[key], func(m logclient.OrganizationMember) bool { return m.ID == id })
}

func removedFromConfig(baseURL string) string {
	return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\norganization = \"\"\n}", baseURL)
}

// A member who leaves after the plan is already gone for the destroy.
func TestLifecycleOrganizationMemberDepartedBeforeDelete(t *testing.T) {
	t.Parallel()

	t.Run("recorded", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a", aliceID, "member"))},
				{Config: removedFromConfig(server.URL), ConfigPlanChecks: planThen(func() { api.removeMember("team-a", aliceID) })},
			},
		})
	})
}

func TestLifecycleOrganizationMemberOrganizationConfirmation(t *testing.T) {
	t.Parallel()

	t.Run("an instance-admin key without organization:read acts through the exchange", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		api.cannotReadOrganization = map[string]bool{"Bearer test-token": true}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a", aliceID, "guest")),
					Check:  resource.TestCheckResourceAttr(memberResource, "organization", "team-a"),
				},
				{Config: memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a", aliceID, "guest")), ConfigPlanChecks: emptyPlan},
			},
		})
		api.assertMemberRequestsIn(t, "team-a")
	})

	t.Run("a key without organization:read and no organization is refused before any write", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		api.cannotReadOrganization = map[string]bool{"Bearer test-token": true}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("", aliceID, "guest")),
				ExpectError: regexp.MustCompile(`(?s)Cannot\s+confirm\s+the\s+organization\s+of\s+the\s+API\s+key.*organization:read`),
			}},
		})
		api.assertWrites(t)
	})

	t.Run("an organization that is not known yet fails the plan", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		config := func(input string) string {
			return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\norganization = \"\"\n}\n"+
				"resource \"terraform_data\" \"organization\" {\ninput = %q\n}\n"+
				"resource \"logfire_organization_member\" \"test\" {\norganization = terraform_data.organization.output\nuser_id = %q\nrole = \"guest\"\n}",
				server.URL, input, aliceID)
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config("team-a")},
				{Config: config("team-b"), ExpectError: regexp.MustCompile(`Organization not known until apply`)},
				{Config: config("team-a"), ConfigPlanChecks: emptyPlan},
			},
		})
		api.assertWrites(t, "team-a PATCH "+aliceID+"=guest", "team-a DELETE "+aliceID)
	})
}

// The create of a replacement has no state. With no organization on the resource it acts in the organization of
// the credential, which must be the organization that the delete leaves.
func TestLifecycleOrganizationMemberReplacementOrganization(t *testing.T) {
	t.Parallel()

	t.Run("a key of another organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: memberOrganizationConfig(server.URL, "team-a-key", "", memberResourceBody("", aliceID, "member"))},
				{
					Config:      memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("", danID, "member")),
					ExpectError: regexp.MustCompile(`(?s)Resource is in another organization.*lives in organization\s+"team-a"`),
				},
				{Config: memberOrganizationConfig(server.URL, "team-a-key", "", memberResourceBody("", aliceID, "member")), ConfigPlanChecks: emptyPlan},
			},
		})
		api.assertWrites(t, "team-a DELETE "+aliceID)
		api.assertRole(t, "admin", danID, "member")
	})

	t.Run("a key that cannot name its organization", func(t *testing.T) {
		t.Parallel()
		server, api := newProjectOrganizationAPI(t)
		api.cannotReadOrganization = map[string]bool{"Bearer test-token": true}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a", aliceID, "member"))},
				{
					Config:      memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("", bobID, "guest")),
					ExpectError: regexp.MustCompile(`Cannot confirm the organization of the replacement`),
				},
				{Config: memberOrganizationConfig(server.URL, "test-token", "", memberResourceBody("team-a", aliceID, "member")), ConfigPlanChecks: emptyPlan},
			},
		})
		api.assertWrites(t, "team-a DELETE "+aliceID)
		api.assertRole(t, "team-a", bobID, "guest")
	})
}
