// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const groupMappingsURL = "/api/v1/instance/group-mappings/"

// groupMappingsAPI mimics the instance routes: a PUT replaces the whole document, checks
// that every organization and project exists, and answers 409 while the Helm value overrides.
type groupMappingsAPI struct {
	sync.Mutex
	// projects maps each existing organization to its project names.
	projects      map[string][]string
	stored        []logclient.GroupMapping
	overridden    bool
	routesMissing bool
	// lostPutResponses makes a PUT commit its mapping and then answer 503.
	lostPutResponses bool
	// redirectPuts answers a PUT with a 301, which the client follows as a GET.
	redirectPuts bool
	gets         int
	puts         []json.RawMessage
}

func newGroupMappingsAPI(t *testing.T) (*httptest.Server, *groupMappingsAPI) {
	t.Helper()
	api := &groupMappingsAPI{
		projects: map[string][]string{"team-a": {"api", "worker"}, "team-b": {}},
		stored:   []logclient.GroupMapping{},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Lock()
		defer api.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != groupMappingsURL {
			// Instance routes take the raw key, so no token exchange may happen.
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want the raw provider key", got)
		}
		if api.redirectPuts && r.Method == http.MethodPut {
			http.Redirect(w, r, groupMappingsURL+"?moved=1", http.StatusMovedPermanently)
			return
		}
		if api.routesMissing {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
			return
		}
		switch r.Method {
		case http.MethodGet:
			api.gets++
			status := logclient.GroupMappingsStatusActive
			if api.overridden {
				status = logclient.GroupMappingsStatusOverriddenByEnvironment
			}
			_ = json.NewEncoder(w).Encode(logclient.GroupMappingsRead{
				GroupOrganizationMapping: api.stored, APIMappingStatus: status,
			})
		case http.MethodPut:
			var raw json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Errorf("PUT body: %v", err)
			}
			api.puts = append(api.puts, raw)
			if api.overridden {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"detail":"The effective group mapping is configured through the environment, so API-managed updates cannot take effect."}`))
				return
			}
			var body struct {
				Mapping []logclient.GroupMapping `json:"group_organization_mapping"`
			}
			if err := json.Unmarshal(raw, &body); err != nil || body.Mapping == nil {
				t.Errorf("PUT must send group_organization_mapping as a list, got %s", raw)
			}
			if detail := api.validate(body.Mapping); detail != "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"detail": detail})
				return
			}
			api.stored = body.Mapping
			if api.lostPutResponses {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"group_organization_mapping": api.stored})
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, api
}

func (api *groupMappingsAPI) validate(mapping []logclient.GroupMapping) string {
	seen := map[string]bool{}
	for _, group := range mapping {
		if seen[group.GroupID] {
			return "Group IDs must be unique"
		}
		seen[group.GroupID] = true
		for _, role := range group.OrganizationRoles {
			projects, ok := api.projects[role.OrganizationName]
			if !ok {
				return fmt.Sprintf("Organization '%s' does not exist", role.OrganizationName)
			}
			for _, project := range role.ProjectRoles {
				if !slices.Contains(projects, project.ProjectName) {
					return fmt.Sprintf("Project '%s' does not exist in organization '%s'", project.ProjectName, role.OrganizationName)
				}
			}
		}
	}
	return ""
}

func (api *groupMappingsAPI) lastPut(t *testing.T) string {
	t.Helper()
	api.Lock()
	defer api.Unlock()
	if len(api.puts) == 0 {
		t.Fatal("no PUT was sent")
	}
	return string(api.puts[len(api.puts)-1])
}

const (
	groupMappingsResourceName = "logfire_group_mappings.test"
	groupMappingsOpsGroup     = `{
  group_id = "platform-ops"
  organization_roles = [{
    organization = "team-a"
    role         = "guest"
    project_roles = [
      { project = "api", role = "read" },
      { project = "worker", role = "write" },
    ]
  }]
}`
	// The first organization role leaves project_roles out and the second sets it to [],
	// so both spellings must produce the same state as the API's [].
	groupMappingsDevGroup = `{
  group_id = "dev"
  organization_roles = [
    { organization = "team-a", role = "member" },
    { organization = "team-b", role = "guest", project_roles = [] },
  ]
}`
)

func groupMappingsConfig(serverURL string, groups ...string) string {
	body := "mapping = ["
	for _, group := range groups {
		body += "\n" + group + ","
	}
	return lifecycleConfig(serverURL, "group_mappings", body+"\n]")
}

// The PUT body uses the instance's field names. The fake decodes with the client's
// own types, so only this exact body pins them.
func TestLifecycleGroupMappingsWireNames(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	const want = `{"group_organization_mapping":[` +
		`{"group_id":"platform-ops","organization_roles":[{"organization_name":"team-a","role":"guest","project_roles":[` +
		`{"project_name":"api","role":"read"},{"project_name":"worker","role":"write"}]}]},` +
		`{"group_id":"dev","organization_roles":[` +
		`{"organization_name":"team-a","role":"member","project_roles":[]},` +
		`{"organization_name":"team-b","role":"guest","project_roles":[]}]}]}`
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: groupMappingsConfig(server.URL, groupMappingsOpsGroup, groupMappingsDevGroup),
			Check: func(*terraform.State) error {
				if got := api.lastPut(t); got != want {
					return fmt.Errorf("PUT body = %s, want %s", got, want)
				}
				return nil
			},
		}},
	})
}

func TestLifecycleGroupMappings(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	both := groupMappingsConfig(server.URL, groupMappingsOpsGroup, groupMappingsDevGroup)
	reordered := groupMappingsConfig(server.URL, groupMappingsDevGroup, groupMappingsOpsGroup)
	noPlan := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(groupMappingsResourceName, plancheck.ResourceActionNoop),
	}}
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(groupMappingsResourceName, plancheck.ResourceActionUpdate),
	}}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: both,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(groupMappingsResourceName, "id", "instance"),
					resource.TestCheckResourceAttr(groupMappingsResourceName, "api_mapping_status", "active"),
					resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.#", "2"),
					resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.0.group_id", "platform-ops"),
					resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.0.organization_roles.0.project_roles.1.project", "worker"),
					resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.1.organization_roles.0.project_roles.#", "0"),
					resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.1.organization_roles.1.project_roles.#", "0"),
				),
			},
			{Config: both, ConfigPlanChecks: noPlan},
			{Config: both, ResourceName: groupMappingsResourceName, ImportState: true, ImportStateId: "instance", ImportStateVerify: true},
			{
				// The server keeps the order it receives.
				Config:           reordered,
				ConfigPlanChecks: update,
				Check:            resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.0.group_id", "dev"),
			},
			{
				// A mapping changed outside Terraform shows as drift.
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.stored[1].OrganizationRoles[0].Role = "admin"
				},
				Config:           reordered,
				ConfigPlanChecks: update,
				Check:            resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.1.organization_roles.0.role", "guest"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.stored) != 0 {
		t.Fatalf("destroy left the mapping %+v", api.stored)
	}
	if got := string(api.puts[len(api.puts)-1]); got != `{"group_organization_mapping":[]}` {
		t.Errorf("destroy sent %s, want an empty list", got)
	}
}

// Create must not replace a mapping that someone else stored, and a refused create
// must not write.
func TestLifecycleGroupMappingsCreateRefusals(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		setup func(*groupMappingsAPI)
		error string
	}{
		"stored mapping": {
			setup: func(api *groupMappingsAPI) {
				api.stored = []logclient.GroupMapping{{GroupID: "other", OrganizationRoles: []logclient.GroupMappingOrganizationRole{}}}
			},
			error: `terraform\s+import\s+logfire_group_mappings\.<name>\s+instance`,
		},
		"stored mapping that differs from the plan in one role": {
			setup: func(api *groupMappingsAPI) {
				api.stored = []logclient.GroupMapping{{
					GroupID: "platform-ops",
					OrganizationRoles: []logclient.GroupMappingOrganizationRole{{
						OrganizationName: "team-a", Role: "member",
						ProjectRoles: []logclient.GroupMappingProjectRole{{ProjectName: "api", Role: "read"}, {ProjectName: "worker", Role: "write"}},
					}},
				}}
			},
			error: `terraform\s+import\s+logfire_group_mappings\.<name>\s+instance`,
		},
		"environment override": {
			setup: func(api *groupMappingsAPI) { api.overridden = true },
			error: `Helm\s+value\s+` + "`groupOrganizationMapping`",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, api := newGroupMappingsAPI(t)
			tc.setup(api)
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      groupMappingsConfig(server.URL, groupMappingsOpsGroup),
					ExpectError: regexp.MustCompile(tc.error),
				}},
			})
			api.Lock()
			defer api.Unlock()
			if len(api.puts) != 0 {
				t.Errorf("a refused create sent %d PUT request(s)", len(api.puts))
			}
		})
	}
}

// A create whose PUT committed but whose response was lost leaves the planned mapping stored.
// The next create refuses it like any stored mapping, because it cannot tell who wrote it.
func TestLifecycleGroupMappingsCreateRefusesTheMappingOfALostResponse(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	config := groupMappingsConfig(server.URL, groupMappingsOpsGroup, groupMappingsDevGroup)
	var putsBeforeRetry int
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { api.Lock(); api.lostPutResponses = true; api.Unlock() },
				Config:      config,
				ExpectError: regexp.MustCompile(`Create group mappings failed`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.lostPutResponses = false
					putsBeforeRetry = len(api.puts)
				},
				Config:      config,
				ExpectError: regexp.MustCompile(`Group mappings already exist`),
			},
		},
	})
	// A step's Check does not run when the step expects an error, so check here.
	api.Lock()
	defer api.Unlock()
	if len(api.puts) != putsBeforeRetry {
		t.Errorf("the refused create sent %d PUT request(s)", len(api.puts)-putsBeforeRetry)
	}
}

// Behind a redirect the client resends the destroy's PUT as a GET, which answers 200 with
// the stored mapping. Destroy reports that instead of dropping the resource.
func TestLifecycleGroupMappingsDestroyThroughARedirectFails(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	config := groupMappingsConfig(server.URL, groupMappingsOpsGroup)
	setRedirect := func(on bool) func() { return func() { api.Lock(); api.redirectPuts = on; api.Unlock() } }
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{PreConfig: setRedirect(true), Config: config, Destroy: true, ExpectError: regexp.MustCompile(`still reports a mapping`)},
			{PreConfig: setRedirect(false), Config: config, PlanOnly: true},
		},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.stored) != 0 {
		t.Errorf("stored = %v after the final destroy, want an empty mapping", api.stored)
	}
}

// Importing the stored mapping adopts it without a write, and the first plan is empty.
func TestLifecycleGroupMappingsAdoptStoredMapping(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	api.stored = []logclient.GroupMapping{{
		GroupID: "platform-ops",
		OrganizationRoles: []logclient.GroupMappingOrganizationRole{{
			OrganizationName: "team-a", Role: "guest",
			ProjectRoles: []logclient.GroupMappingProjectRole{{ProjectName: "api", Role: "read"}, {ProjectName: "worker", Role: "write"}},
		}},
	}}
	config := groupMappingsConfig(server.URL, groupMappingsOpsGroup)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, ResourceName: groupMappingsResourceName, ImportState: true, ImportStateId: "instance", ImportStatePersist: true},
			{Config: config, PlanOnly: true},
		},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.puts) != 1 {
		t.Errorf("PUT requests = %d, want only the one from destroy", len(api.puts))
	}
}

func TestLifecycleGroupMappingsImportRejectsOtherIDs(t *testing.T) {
	t.Parallel()
	server, _ := newGroupMappingsAPI(t)
	config := groupMappingsConfig(server.URL, groupMappingsOpsGroup)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, ResourceName: groupMappingsResourceName, ImportState: true, ImportStateId: "other", ExpectError: regexp.MustCompile(`Expected\s+"instance"`)},
		},
	})
}

func TestLifecycleGroupMappingsEmptyList(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	config := groupMappingsConfig(server.URL)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttr(groupMappingsResourceName, "mapping.#", "0")},
			{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(groupMappingsResourceName, plancheck.ResourceActionNoop),
			}}},
		},
	})
	if got := api.lastPut(t); got != `{"group_organization_mapping":[]}` {
		t.Errorf("last PUT = %s", got)
	}
}

func TestLifecycleGroupMappingsRejections(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	group := func(id, org, role string) string {
		return fmt.Sprintf(`{ group_id = %q, organization_roles = [{ organization = %q, role = %q }] }`, id, org, role)
	}
	for name, tc := range map[string]struct {
		config string
		error  string
	}{
		"repeated group_id is caught at plan time": {
			config: groupMappingsConfig(server.URL, group("ops", "team-a", "guest"), group("ops", "team-b", "guest")),
			error:  `group_id\s+"ops"\s+already\s+appears\s+at\s+index\s+0`,
		},
		"empty role": {
			config: groupMappingsConfig(server.URL, group("ops", "team-a", "")),
			error:  `at\s+least\s+1`,
		},
		"missing organization is the API's 400": {
			config: groupMappingsConfig(server.URL, group("ops", "nowhere", "guest")),
			error:  `rejected\s+the\s+mapping:\s+Organization\s+'nowhere'\s+does\s+not\s+exist`,
		},
		"missing project is the API's 400": {
			config: groupMappingsConfig(server.URL, `{ group_id = "ops", organization_roles = [{ organization = "team-a", role = "guest", project_roles = [{ project = "gone", role = "read" }] }] }`),
			error:  `Project\s+'gone'\s+does\s+not\s+exist\s+in\s+organization\s+'team-a'`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{Config: tc.config, ExpectError: regexp.MustCompile(tc.error)}},
			})
		})
	}
	api.Lock()
	defer api.Unlock()
	if len(api.stored) != 0 {
		t.Errorf("a rejected mapping was stored: %+v", api.stored)
	}
}

// The Helm value can be set after Terraform created the resource. The API then refuses
// writes with 409, and destroy fails instead of leaving the stored mapping in place unnoticed.
func TestLifecycleGroupMappingsOverriddenAfterCreate(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	setOverridden := func(value bool) func() {
		return func() {
			api.Lock()
			defer api.Unlock()
			api.overridden = value
		}
	}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupMappingsConfig(server.URL, groupMappingsOpsGroup)},
			{
				PreConfig:   setOverridden(true),
				Config:      groupMappingsConfig(server.URL, groupMappingsDevGroup),
				ExpectError: regexp.MustCompile(`Helm\s+value\s+` + "`groupOrganizationMapping`"),
			},
			{
				// A refresh keeps the stored mapping and reports the status.
				Config: groupMappingsConfig(server.URL, groupMappingsOpsGroup),
				Check:  resource.TestCheckResourceAttr(groupMappingsResourceName, "api_mapping_status", "overridden_by_environment"),
			},
			{
				Config: groupMappingsConfig(server.URL, groupMappingsOpsGroup), Destroy: true,
				ExpectError: regexp.MustCompile(`terraform\s+state\s+rm`),
			},
			{PreConfig: setOverridden(false), Config: groupMappingsConfig(server.URL, groupMappingsOpsGroup)},
		},
	})
	api.Lock()
	defer api.Unlock()
	if !reflect.DeepEqual(api.stored, []logclient.GroupMapping{}) {
		t.Errorf("destroy left the mapping %+v", api.stored)
	}
}

func TestLifecycleGroupMappingsOnOlderLogfire(t *testing.T) {
	t.Parallel()
	server, api := newGroupMappingsAPI(t)
	api.routesMissing = true
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      groupMappingsConfig(server.URL, groupMappingsOpsGroup),
			ExpectError: regexp.MustCompile(`requires\s+Logfire\s+v2026-07-22\.02\s+or\s+newer\s+\(Helm\s+chart\s+logfire-0\.13\.36`),
		}},
	})
}
