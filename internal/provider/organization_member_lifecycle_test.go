// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	aliceID = "0191a5c0-0000-7000-8000-00000000a11c"
	bobID   = "0191a5c0-0000-7000-8000-0000000000b0"
	carolID = "0191a5c0-0000-7000-8000-0000000000ca"
)

type membersAPI struct {
	sync.Mutex
	members  []logclient.OrganizationMember
	requests map[string]int
	patches  []string
	// ownOrganizationStatus makes the request for the organization of the key fail with that status.
	ownOrganizationStatus int
	// refuse answers the next write with this status and detail.
	refuseStatus int
	refuseDetail string
}

// newMembersAPI mimics the public members API. A PATCH accepts only the built-in roles,
// and a user who leaves the list is no longer a member.
func newMembersAPI(t *testing.T) (*httptest.Server, *membersAPI) {
	t.Helper()
	api := &membersAPI{
		requests: map[string]int{},
		members: []logclient.OrganizationMember{
			{ID: aliceID, Name: "Alice", Email: "alice@example.com", Role: "member", MemberSince: "2026-01-01T00:00:00Z"},
			{ID: bobID, Name: "Bob", Email: "bob@example.com", Role: "guest", MemberSince: "2026-01-02T00:00:00Z"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Lock()
		defer api.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == ownOrganizationPath && r.Method == http.MethodGet {
			if api.ownOrganizationStatus != 0 {
				w.WriteHeader(api.ownOrganizationStatus)
				_, _ = w.Write([]byte(`{"detail":"organization unavailable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"org-own","organization_name":"own"}`))
			return
		}
		api.requests[r.Method+" "+r.URL.Path]++
		if r.URL.Path == "/api/v1/members/" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(api.members)
			return
		}
		id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/members/")
		id = strings.TrimSuffix(id, "/")
		if !ok || id == "" {
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if api.refuseStatus != 0 && (r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
			w.WriteHeader(api.refuseStatus)
			_, _ = fmt.Fprintf(w, `{"detail":%q}`, api.refuseDetail)
			return
		}
		index := -1
		for i := range api.members {
			if api.members[i].ID == id {
				index = i
			}
		}
		if index < 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Organization member not found"}`))
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var body struct {
				Role string `json:"role"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("PATCH body: %v", err)
			}
			switch body.Role {
			case "admin", "member", "guest", "billing":
			default:
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"detail":[{"loc":["body","role"],"msg":"Input should be 'admin', 'member', 'guest' or 'billing'"}]}`))
				return
			}
			api.members[index].Role = body.Role
			api.patches = append(api.patches, id+"="+body.Role)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			api.members = append(api.members[:index], api.members[index+1:]...)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, api
}

// aliceRole reads the role of Alice, the member that most tests manage. The caller holds the lock.
func (a *membersAPI) aliceRole() (string, bool) {
	for _, m := range a.members {
		if m.ID == aliceID {
			return m.Role, true
		}
	}
	return "", false
}

func memberConfig(serverURL, userID, role string) string {
	return lifecycleConfig(serverURL, "organization_member", fmt.Sprintf("user_id = %q\nrole = %q", userID, role))
}

const memberResource = "logfire_organization_member.test"

// Adopting a member who already has the configured role writes nothing, and destroy removes the user.
func TestLifecycleOrganizationMemberAdoptSameRole(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "member")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(memberResource, "id", aliceID),
					resource.TestCheckResourceAttr(memberResource, "email", "alice@example.com"),
					resource.TestCheckResourceAttr(memberResource, "role", "member"),
					resource.TestCheckResourceAttr(memberResource, "name", "Alice"),
				),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(memberResource, plancheck.ResourceActionNoop),
				}},
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.patches) != 0 {
		t.Errorf("PATCH requests = %v, want none", api.patches)
	}
	if _, found := api.aliceRole(); found {
		t.Error("destroy must remove the member from the organization")
	}
}

func TestLifecycleOrganizationMemberAdoptNewRoleAndUpdate(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: memberConfig(server.URL, aliceID, "admin"),
				Check:  resource.TestCheckResourceAttr(memberResource, "role", "admin"),
			},
			{
				Config: memberConfig(server.URL, aliceID, "billing"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(memberResource, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(memberResource, "role", "billing"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if want := []string{aliceID + "=admin", aliceID + "=billing"}; !slices.Equal(api.patches, want) {
		t.Errorf("PATCH requests = %v, want %v", api.patches, want)
	}
}

func TestLifecycleOrganizationMemberMissingUser(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      memberConfig(server.URL, carolID, "member"),
			ExpectError: regexp.MustCompile(`(?s)not\s+a\s+member.*must\s+already.*group\s+mapping.*SCIM.*invitation`),
		}},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.patches) != 0 || api.requests["DELETE /api/v1/members/"+aliceID+"/"] != 0 {
		t.Errorf("a missing user must cause no write, got patches %v", api.patches)
	}
}

// A member who leaves outside Terraform is dropped from state, so the plan creates the resource again.
func TestLifecycleOrganizationMemberGoneOnRead(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "member")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.members = api.members[1:]
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(memberResource, plancheck.ResourceActionCreate),
				}},
				ExpectError: regexp.MustCompile(`not\s+a\s+member`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.members = append(api.members, logclient.OrganizationMember{ID: aliceID, Name: "Alice", Email: "alice@example.com", Role: "member"})
				},
				Config: config,
			},
		},
	})
}

func TestLifecycleOrganizationMemberImport(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "guest")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, ResourceName: memberResource, ImportState: true, ImportStateId: aliceID, ImportStateVerify: true},
			{
				Config: config, ResourceName: memberResource, ImportState: true, ImportStateId: carolID,
				ExpectError: regexp.MustCompile(`no\s+member\s+with\s+the\s+user\s+ID`),
			},
			{
				Config: config, ResourceName: memberResource, ImportState: true, ImportStateId: "alice@example.com",
				ExpectError: regexp.MustCompile(`Invalid import ID format`),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if role, found := api.aliceRole(); found {
		t.Errorf("destroy must remove the member, role = %q", role)
	}
}

// A 404 without the member route's own detail means that the route is missing, as on an
// older backend. Destroy must fail, because the user still has access.
func TestLifecycleOrganizationMemberDestroyMissingRoute(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "member")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.refuseStatus, api.refuseDetail = http.StatusNotFound, "Not Found"
				},
				Config:      config,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)removing\s+the\s+member.*HTTP\s+404`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.refuseStatus = 0
				},
				Config: config,
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if _, found := api.aliceRole(); found {
		t.Error("the retried destroy must remove the member")
	}
}

// Someone sets a custom role in the UI. Read stores its name, and the next apply sets the built-in role again.
func TestLifecycleOrganizationMemberCustomRole(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "member")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.members[0].Role = "auditor"
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(memberResource, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(memberResource, "role", "member"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if role, found := api.aliceRole(); found {
		t.Errorf("destroy must remove the member, role = %q", role)
	}
}

func TestLifecycleOrganizationMemberRejectsUnknownRole(t *testing.T) {
	t.Parallel()
	server, _ := newMembersAPI(t)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      memberConfig(server.URL, aliceID, "auditor"),
			ExpectError: regexp.MustCompile(`(?s)admin.*member.*guest.*billing`),
		}},
	})
}

func TestLifecycleOrganizationMemberWriteErrors(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		detail string
		want   string
	}{
		"last admin": {http.StatusBadRequest, "More than one admin is required", `(?s)more\s+than\s+one\s+admin.*Make\s+another\s+member\s+an\s+admin`},
		"seat limit": {http.StatusPaymentRequired, "Organization guest limit reached. Ask an admin to free a guest seat or upgrade the plan.", `(?s)no\s+free\s+seat.*guest\s+limit\s+reached`},
		"personal org owner": {
			http.StatusUnprocessableEntity, "The owner of a personal organization must remain an admin.",
			`(?s)does\s+not\s+allow.*owner\s+of\s+a\s+personal\s+organization`,
		},
		"escalation": {http.StatusForbidden, "You cannot assign a role with more permissions than your own.", `(?s)more\s+permissions\s+than\s+its\s+own.*organization:write_member.*HTTP\s+403`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, api := newMembersAPI(t)
			api.refuseStatus, api.refuseDetail = tc.status, tc.detail
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      memberConfig(server.URL, aliceID, "admin"),
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
			api.Lock()
			defer api.Unlock()
			if len(api.patches) != 0 {
				t.Errorf("a refused PATCH must change nothing, got %v", api.patches)
			}
		})
	}
}

// A failed removal keeps the resource in state, so the destroy reports the 400 and can be retried.
func TestLifecycleOrganizationMemberDestroyRefused(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "admin")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.refuseStatus, api.refuseDetail = http.StatusBadRequest, "More than one admin is required"
				},
				Config:      config,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)removing\s+the\s+member.*more\s+than\s+one\s+admin`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.refuseStatus = 0
				},
				Config: config,
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if _, found := api.aliceRole(); found {
		t.Error("the retried destroy must remove the member")
	}
}

func TestLifecycleOrganizationMembersDataSource(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	api.members = append(api.members, logclient.OrganizationMember{ID: "0191a5c0-0000-7000-8000-0000000000c0", Name: "Cy", Email: "Cy@example.com", Role: "auditor"})
	config := fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\n}\ndata \"logfire_organization_members\" \"all\" {}", server.URL)
	const name = "data.logfire_organization_members.all"
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(name, "members.#", "3"),
				resource.TestCheckResourceAttr(name, "members.0.email", "alice@example.com"),
				resource.TestCheckResourceAttr(name, "members.0.id", aliceID),
				resource.TestCheckResourceAttr(name, "members.0.name", "Alice"),
				resource.TestCheckResourceAttr(name, "members.0.role", "member"),
				resource.TestCheckResourceAttr(name, "members.1.email", "bob@example.com"),
				resource.TestCheckResourceAttr(name, "members.1.role", "guest"),
				resource.TestCheckResourceAttr(name, "members.2.email", "Cy@example.com"),
				resource.TestCheckResourceAttr(name, "members.2.role", "auditor"),
			),
		}},
	})
}

// Another user ID replaces the resource: the old member leaves the organization and the new one is adopted.
func TestLifecycleOrganizationMemberUserIDReplaces(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: memberConfig(server.URL, aliceID, "member")},
			{
				Config: memberConfig(server.URL, bobID, "member"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(memberResource, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr(memberResource, "id", bobID),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if _, found := api.aliceRole(); found {
		t.Error("replacement must remove the previous member")
	}
}

// The email and the name are read for information. A new address of the user shows in state with no plan change.
func TestLifecycleOrganizationMemberEmailIsInformation(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "member")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttr(memberResource, "email", "alice@example.com")},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.members[0].Email = "alice@renamed.example.com"
					api.members[0].Name = "Alice Renamed"
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(memberResource, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(memberResource, "email", "alice@renamed.example.com"),
					resource.TestCheckResourceAttr(memberResource, "name", "Alice Renamed"),
				),
			},
		},
	})
}

// A key that cannot name its organization is refused before any write, whether it lacks organization:read
// or the instance does not answer.
func TestLifecycleOrganizationMemberCreateNeedsTheOrganizationOfTheKey(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		want   string
	}{
		"no organization:read": {http.StatusForbidden, `(?s)Cannot\s+confirm\s+the\s+organization.*organization:read`},
		"no answer":            {http.StatusInternalServerError, `Cannot\s+read\s+the\s+organization\s+of\s+the\s+API\s+key`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, api := newMembersAPI(t)
			api.ownOrganizationStatus = tc.status
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      memberConfig(server.URL, aliceID, "admin"),
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
			api.Lock()
			defer api.Unlock()
			if len(api.patches) != 0 {
				t.Errorf("an unconfirmed organization must cause no write, got %v", api.patches)
			}
		})
	}
}

// An import without a confirmable organization is refused as well, so state never names a member that no record places.
func TestLifecycleOrganizationMemberImportNeedsTheOrganizationOfTheKey(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := memberConfig(server.URL, aliceID, "member")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.ownOrganizationStatus = http.StatusForbidden
				},
				Config: config, ResourceName: memberResource, ImportState: true, ImportStateId: aliceID,
				ExpectError: regexp.MustCompile(`(?s)Cannot\s+confirm\s+the\s+organization.*organization:read`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.ownOrganizationStatus = 0
				},
				Config: config,
			},
		},
	})
}

// The email address is not an input. A value that is not a user ID fails at plan time.
func TestLifecycleOrganizationMemberRejectsAnEmailAsUserID(t *testing.T) {
	t.Parallel()
	server, _ := newMembersAPI(t)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      memberConfig(server.URL, "alice@example.com", "member"),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`lowercase\s+UUID`),
		}},
	})
}

// A user ID that is not known at plan time would replace the member, which removes the user before the
// new one is looked up, so the plan fails instead.
func TestLifecycleOrganizationMemberUnknownUserID(t *testing.T) {
	t.Parallel()
	server, api := newMembersAPI(t)
	config := func(input string) string {
		return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = \"test-token\"\norganization = \"\"\n}\n"+
			"resource \"terraform_data\" \"user\" {\ninput = %q\n}\n"+
			"resource \"logfire_organization_member\" \"test\" {\nuser_id = terraform_data.user.output\nrole = \"member\"\n}", server.URL, input)
	}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(aliceID)},
			{Config: config(bobID), ExpectError: regexp.MustCompile(`User ID not known until apply`)},
			{Config: config(aliceID), ConfigPlanChecks: emptyPlan},
		},
	})
	api.Lock()
	defer api.Unlock()
	if got := api.requests["DELETE /api/v1/members/"+aliceID+"/"]; got != 1 {
		t.Errorf("DELETE requests = %d, want 1 (destroy only)", got)
	}
}
