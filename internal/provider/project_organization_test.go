// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const testAPIKey = "test-token"

type testProject struct {
	ID, Name, Description, Visibility, OrganizationID string
}

// organizationInstance is a self-hosted instance with several organizations. The
// API key belongs to the "admin" organization and can be exchanged for any other.
type organizationInstance struct {
	*httptest.Server
	sync.Mutex
	organizations map[string]string // ID to name
	projects      map[string]*testProject
	exchanges     int
	creates       int
	deletes       int
	nextProject   int
	// exchangeNeedsAdmin makes the token endpoint refuse the key as a non-admin key.
	exchangeNeedsAdmin bool
	// projectListRefused makes the project list answer 403.
	projectListRefused bool
	// proxyForbids makes a project read answer a 403 that the instance did not send.
	proxyForbids bool
}

func newOrganizationInstance(t *testing.T, names ...string) *organizationInstance {
	t.Helper()
	api := &organizationInstance{organizations: map[string]string{"org-admin": "admin"}, projects: map[string]*testProject{}}
	for _, name := range names {
		api.organizations["org-"+name] = name
	}
	api.Server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.Close)
	return api
}

func (api *organizationInstance) rename(oldName, newName string) {
	api.Lock()
	defer api.Unlock()
	api.organizations["org-"+oldName] = newName
}

func (api *organizationInstance) addOrganization(id, name string) {
	api.Lock()
	defer api.Unlock()
	api.organizations[id] = name
}

func (api *organizationInstance) deleteOrganization(name string) {
	api.Lock()
	defer api.Unlock()
	id := "org-" + name
	delete(api.organizations, id)
	for projectID, project := range api.projects {
		if project.OrganizationID == id {
			delete(api.projects, projectID)
		}
	}
}

func (api *organizationInstance) projectsIn(name string) []string {
	api.Lock()
	defer api.Unlock()
	var names []string
	for _, project := range api.projects {
		if api.organizations[project.OrganizationID] == name {
			names = append(names, project.Name)
		}
	}
	return names
}

func (api *organizationInstance) serve(w http.ResponseWriter, r *http.Request) {
	api.Lock()
	defer api.Unlock()
	reply := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	projectJSON := func(p *testProject) map[string]any {
		return map[string]any{
			"id": p.ID, "project_name": p.Name, "description": nilIfEmpty(p.Description),
			"visibility": p.Visibility, "organization_name": api.organizations[p.OrganizationID],
		}
	}
	authorization := r.Header.Get("Authorization")
	callerOrganization := ""
	switch {
	case authorization == "Bearer "+testAPIKey:
		callerOrganization = "org-admin"
	case strings.HasPrefix(authorization, "Bearer tok:"):
		callerOrganization = strings.TrimPrefix(authorization, "Bearer tok:")
	}

	if r.URL.Path == "/api/oauth/token" {
		_ = r.ParseForm()
		api.exchanges++
		if api.exchangeNeedsAdmin {
			reply(http.StatusBadRequest, map[string]string{"error": "invalid_scope", "error_description": "subject_token must include instance:admin"})
			return
		}
		name := strings.TrimPrefix(r.PostForm.Get("audience"), "http://"+r.Host+"/")
		for id, organizationName := range api.organizations {
			if organizationName == name {
				reply(http.StatusOK, map[string]any{"access_token": "tok:" + id, "expires_in": 900})
				return
			}
		}
		reply(http.StatusBadRequest, map[string]string{"error": "invalid_target", "error_description": "unknown organization: '" + name + "'"})
		return
	}
	if r.URL.Path == "/api/v1/instance/organizations/" {
		if authorization != "Bearer "+testAPIKey {
			reply(http.StatusForbidden, map[string]string{"detail": "instance:admin is required"})
			return
		}
		if r.Method == http.MethodPost {
			var in struct {
				Name string `json:"organization_name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			id := "org-" + in.Name
			api.organizations[id] = in.Name
			reply(http.StatusCreated, map[string]any{"id": id, "organization_name": in.Name})
			return
		}
		var list []map[string]any
		for id, name := range api.organizations {
			list = append(list, map[string]any{"id": id, "organization_name": name})
		}
		reply(http.StatusOK, list)
		return
	}
	if callerOrganization == "" || api.organizations[callerOrganization] == "" {
		reply(http.StatusUnauthorized, map[string]string{"detail": "invalid token"})
		return
	}

	if r.URL.Path == "/api/v1/organization/" {
		organization := map[string]any{"id": callerOrganization, "organization_name": api.organizations[callerOrganization]}
		switch r.Method {
		case http.MethodGet:
			reply(http.StatusOK, organization)
		case http.MethodPut:
			var in struct {
				Name *string `json:"organization_name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Name != nil {
				api.organizations[callerOrganization] = *in.Name
			}
			organization["organization_name"] = api.organizations[callerOrganization]
			reply(http.StatusOK, organization)
		case http.MethodDelete:
			delete(api.organizations, callerOrganization)
			reply(http.StatusNoContent, nil)
		}
		return
	}

	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"), "/")
	switch {
	case r.URL.Path == "/api/v1/projects/" && r.Method == http.MethodGet && api.projectListRefused:
		reply(http.StatusForbidden, map[string]string{"detail": "This token is missing the required scope: project:read."})
	case r.URL.Path == "/api/v1/projects/" && r.Method == http.MethodGet:
		list := []map[string]any{}
		for _, project := range api.projects {
			if project.OrganizationID == callerOrganization {
				list = append(list, projectJSON(project))
			}
		}
		reply(http.StatusOK, list)
	case r.URL.Path == "/api/v1/projects/" && r.Method == http.MethodPost:
		var in struct {
			ProjectName string  `json:"project_name"`
			Description *string `json:"description"`
			Visibility  *string `json:"visibility"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		api.nextProject++
		project := &testProject{ID: fmt.Sprintf("project-%d", api.nextProject), Name: in.ProjectName, Visibility: "public", OrganizationID: callerOrganization}
		if in.Description != nil {
			project.Description = *in.Description
		}
		if in.Visibility != nil {
			project.Visibility = *in.Visibility
		}
		api.projects[project.ID] = project
		api.creates++
		reply(http.StatusCreated, projectJSON(project))
	case strings.HasPrefix(r.URL.Path, "/api/v1/projects/") && api.proxyForbids:
		reply(http.StatusForbidden, map[string]string{"detail": "Blocked by proxy"})
	case strings.HasPrefix(r.URL.Path, "/api/v1/projects/"):
		project := api.projects[id]
		if project == nil || project.OrganizationID != callerOrganization {
			// Like the server, answer 403 for a missing project or one in another organization.
			reply(http.StatusForbidden, map[string]string{"detail": "You do not have permission to access this project"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			reply(http.StatusOK, projectJSON(project))
		case http.MethodPut:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if v, ok := in["project_name"].(string); ok {
				project.Name = v
			}
			if v, ok := in["description"].(string); ok {
				project.Description = v
			}
			if v, ok := in["visibility"].(string); ok {
				project.Visibility = v
			}
			reply(http.StatusOK, projectJSON(project))
		case http.MethodDelete:
			delete(api.projects, id)
			api.deletes++
			reply(http.StatusNoContent, nil)
		}
	default:
		reply(http.StatusNotFound, map[string]string{"detail": "Not Found"})
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func providerBlock(api *organizationInstance, organization string) string {
	var extra string
	if organization != "" {
		extra += fmt.Sprintf("organization = %q\n", organization)
	}
	return fmt.Sprintf("provider \"logfire\" {\nbase_url = %q\napi_key = %q\n%s}\n", api.URL, testAPIKey, extra)
}

func projectBlock(label, body string) string {
	return fmt.Sprintf("resource \"logfire_project\" %q {\n%s\n}\n", label, body)
}

func expectPlan(address string, action plancheck.ResourceActionType) tfresource.ConfigPlanChecks {
	return tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, action)}}
}

var expectNoChange = tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}

func runProjectLifecycle(t *testing.T, steps []tfresource.TestStep) {
	t.Helper()
	tfresource.Test(t, tfresource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
}

func TestLifecycleProjectsInTheProviderAndAnotherOrganization(t *testing.T) {
	api := newOrganizationInstance(t, "team-a", "team-b")
	config := providerBlock(api, "team-a") +
		projectBlock("a", "name = \"svc\"") +
		projectBlock("b", "organization = \"team-b\"\nname = \"svc\"")
	runProjectLifecycle(t, []tfresource.TestStep{
		{
			Config: config,
			Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("logfire_project.a", "organization", "team-a"),
				tfresource.TestCheckResourceAttr("logfire_project.a", "organization_id", "org-team-a"),
				tfresource.TestCheckResourceAttr("logfire_project.b", "organization", "team-b"),
				tfresource.TestCheckResourceAttr("logfire_project.b", "organization_id", "org-team-b"),
			),
		},
		{Config: config, ConfigPlanChecks: expectNoChange},
		{Config: config, ResourceName: "logfire_project.a", ImportState: true, ImportStateId: "team-a/svc", ImportStateVerify: true},
		{Config: config, ResourceName: "logfire_project.b", ImportState: true, ImportStateId: "team-b/svc", ImportStateVerify: true},
	})
	for _, name := range []string{"team-a", "team-b", "admin"} {
		if left := api.projectsIn(name); len(left) != 0 {
			t.Errorf("organization %s still holds %v after destroy", name, left)
		}
	}
	if api.creates != 2 || api.deletes != 2 {
		t.Errorf("creates = %d, deletes = %d, want 2 and 2", api.creates, api.deletes)
	}
}

// A provider block without organization uses LOGFIRE_ORGANIZATION, and one with
// organization ignores it.
func TestLifecycleProjectOrganizationFromTheEnvironment(t *testing.T) {
	for name, tc := range map[string]struct{ provider, want string }{
		"provider block omits organization": {"", "team-a"},
		"provider block wins":               {"team-b", "team-b"},
	} {
		t.Run(name, func(t *testing.T) {
			api := newOrganizationInstance(t, "team-a", "team-b")
			t.Setenv("LOGFIRE_ORGANIZATION", "team-a")
			runProjectLifecycle(t, []tfresource.TestStep{{
				Config: providerBlock(api, tc.provider) + projectBlock("svc", "name = \"svc\""),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("logfire_project.svc", "organization", tc.want),
					tfresource.TestCheckResourceAttr("logfire_project.svc", "organization_id", "org-"+tc.want),
				),
			}})
			if left := api.projectsIn(tc.want); len(left) != 0 {
				t.Errorf("organization %s still holds %v after destroy", tc.want, left)
			}
		})
	}
}

func TestLifecycleProjectOrganizationArgument(t *testing.T) {
	api := newOrganizationInstance(t, "team-a", "team-b")
	config := func(organization, description string) string {
		return providerBlock(api, "") + projectBlock("svc", fmt.Sprintf("organization = %q\nname = \"svc\"\ndescription = %q", organization, description))
	}
	const address = "logfire_project.svc"
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config("team-a", "one"), Check: tfresource.TestCheckResourceAttr(address, "organization_id", "org-team-a")},
		{Config: config("team-a", "one"), ConfigPlanChecks: expectNoChange},
		{Config: config("team-a", "two"), ConfigPlanChecks: expectPlan(address, plancheck.ResourceActionUpdate), Check: tfresource.TestCheckResourceAttr(address, "description", "two")},
		{Config: config("team-a", "two"), ResourceName: address, ImportState: true, ImportStateId: "team-a/svc", ImportStateVerify: true},
		{
			Config:           config("team-b", "two"),
			ConfigPlanChecks: expectPlan(address, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            tfresource.TestCheckResourceAttr(address, "organization_id", "org-team-b"),
		},
	})
	if left := api.projectsIn("team-a"); len(left) != 0 {
		t.Errorf("team-a still holds %v", left)
	}
}

func TestLifecycleProjectMoveIntoMissingOrganizationFailsAtApplyAndDeletesNothing(t *testing.T) {
	api := newOrganizationInstance(t, "team-a")
	config := func(organization string) string {
		return providerBlock(api, "") + projectBlock("svc", fmt.Sprintf("organization = %q\nname = \"svc\"", organization))
	}
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config("team-a")},
		{Config: config("team-gone"), ExpectError: regexp.MustCompile(`(?s)Update project failed.*team-gone`)},
		{
			Config: config("team-a"), ConfigPlanChecks: expectNoChange,
			Check: func(*terraform.State) error {
				if api.deletes != 0 || len(api.projectsIn("team-a")) != 1 {
					return fmt.Errorf("the project was deleted: deletes = %d", api.deletes)
				}
				return nil
			},
		},
	})
}

func TestLifecycleProjectFollowsAnOrganizationRename(t *testing.T) {
	api := newOrganizationInstance(t, "team-a")
	const address = "logfire_project.svc"
	var projectID string
	remember := func(s *terraform.State) error {
		projectID = s.RootModule().Resources[address].Primary.ID
		return nil
	}
	providerOrganization := providerBlock(api, "team-a") + projectBlock("svc", "name = \"svc\"")
	argument := func(organization string) string {
		return providerBlock(api, "") + projectBlock("svc", fmt.Sprintf("organization = %q\nname = \"svc\"", organization))
	}
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: providerOrganization, Check: remember},
		{
			PreConfig:        func() { api.rename("team-a", "team-z") },
			Config:           providerOrganization,
			ConfigPlanChecks: expectNoChange,
			Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr(address, "organization", "team-z"),
				tfresource.TestCheckResourceAttr(address, "organization_id", "org-team-a"),
				func(s *terraform.State) error {
					if got := s.RootModule().Resources[address].Primary.ID; got != projectID {
						return fmt.Errorf("project ID = %s, want %s", got, projectID)
					}
					return nil
				},
			),
		},
		// The old name no longer exists, so a configuration that still has it plans an in-place update that fails.
		{Config: argument("team-a"), ExpectError: regexp.MustCompile(`(?s)Update project failed.*organization\s+"team-a"\s+does\s+not\s+exist\s+on\s+this\s+instance;\s+if\s+it\s+was\s+renamed,\s+set\s+the\s+new\s+name`)},
		{Config: argument("team-z"), ConfigPlanChecks: expectNoChange},
	})
	if api.creates != 1 {
		t.Errorf("creates = %d, want the rename to keep the project", api.creates)
	}
}

func TestLifecycleProjectFollowsARenameWhenAnotherOrganizationTakesTheOldName(t *testing.T) {
	api := newOrganizationInstance(t, "team-a")
	const address = "logfire_project.svc"
	config := providerBlock(api, "team-a") + projectBlock("svc", "name = \"svc\"")
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config},
		{
			PreConfig: func() {
				api.rename("team-a", "team-z")
				api.addOrganization("org-team-a-new", "team-a")
			},
			Config:           config,
			ConfigPlanChecks: expectNoChange,
			Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr(address, "organization", "team-z"),
				tfresource.TestCheckResourceAttr(address, "organization_id", "org-team-a"),
			),
		},
	})
	if api.creates != 1 {
		t.Errorf("creates = %d, want the project kept in the renamed organization", api.creates)
	}
}

func TestLifecycleProjectLeavesStateWhenItsOrganizationIsDeleted(t *testing.T) {
	api := newOrganizationInstance(t, "team-a")
	config := providerBlock(api, "team-a") + projectBlock("svc", "name = \"svc\"")
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config},
		// The refresh drops the project, so the plan creates it again, and that create fails
		// because the organization is gone. A project that stayed in state would fail the read instead.
		{
			PreConfig:   func() { api.deleteOrganization("team-a") },
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)Create project failed.*does not exist`),
		},
	})
}

func TestLifecycleProjectDeletedOutsideTerraformIsCreatedAgain(t *testing.T) {
	api := newOrganizationInstance(t, "team-a")
	config := providerBlock(api, "team-a") + projectBlock("svc", "name = \"svc\"")
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config},
		{
			PreConfig: func() { clear(api.projects) },
			Config:    config,
		},
	})
	if api.creates != 2 {
		t.Errorf("creates = %d, want the deleted project created again", api.creates)
	}
}

func TestLifecycleProjectKeepsStateOnAnotherForbiddenAnswer(t *testing.T) {
	api := newOrganizationInstance(t, "team-a")
	config := providerBlock(api, "team-a") + projectBlock("svc", "name = \"svc\"")
	setProxy := func(on bool) func() { return func() { api.Lock(); api.proxyForbids = on; api.Unlock() } }
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config},
		{PreConfig: setProxy(true), Config: config, ExpectError: regexp.MustCompile(`Blocked by proxy`)},
		{PreConfig: setProxy(false), Config: config, ConfigPlanChecks: expectNoChange},
	})
	if api.creates != 1 {
		t.Errorf("creates = %d, want the project kept in state", api.creates)
	}
}

func TestLifecycleProjectWithoutOrganizationUsesTheKey(t *testing.T) {
	api := newOrganizationInstance(t)
	config := providerBlock(api, "") + projectBlock("svc", "name = \"svc\"")
	const address = "logfire_project.svc"
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config, Check: tfresource.ComposeTestCheckFunc(
			tfresource.TestCheckResourceAttr(address, "organization", "admin"),
			tfresource.TestCheckNoResourceAttr(address, "organization_id"),
		)},
		{Config: config, ConfigPlanChecks: expectNoChange},
		{Config: config, ResourceName: address, ImportState: true, ImportStateId: "admin/svc", ImportStateVerify: true},
	})
	if api.exchanges != 0 {
		t.Errorf("a provider without an organization made %d token exchanges", api.exchanges)
	}
}

func TestProjectOrganizationNamesAreValidated(t *testing.T) {
	api := newOrganizationInstance(t)
	for name, tc := range map[string]struct{ config, want string }{
		"resource": {providerBlock(api, "") + projectBlock("svc", "organization = \"team-a/\"\nname = \"svc\""), "lowercase letters"},
		"provider": {providerBlock(api, "team-a/") + projectBlock("svc", "name = \"svc\""), "not an organization name"},
	} {
		t.Run(name, func(t *testing.T) {
			tfresource.Test(t, tfresource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []tfresource.TestStep{{Config: tc.config, PlanOnly: true, ExpectError: regexp.MustCompile(tc.want)}},
			})
		})
	}
}

// A new organization name replaces the project only when it is another
// organization. Refresh updates the name after a rename, so a plan without
// refresh is the only way to reach the rename case through Terraform.
func TestProjectPlanReplacesOnlyForAnotherKnownOrganization(t *testing.T) {
	api := newOrganizationInstance(t, "team-a", "team-b")
	api.rename("team-a", "team-renamed")
	client, err := logclient.NewAPIClient(api.URL, testAPIKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &ProjectResource{client: client}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	schema := schemaResponse.Schema
	state := func(organization, organizationID types.String) tfsdk.State {
		s := tfsdk.State{Schema: schema, Raw: tftypes.NewValue(schema.Type().TerraformType(t.Context()), nil)}
		if diags := s.Set(t.Context(), &ProjectModel{
			ID: types.StringValue("project-1"), Organization: organization, OrganizationID: organizationID,
			Name: types.StringValue("svc"), Description: types.StringNull(), Visibility: types.StringValue("public"),
		}); diags.HasError() {
			t.Fatal(diags)
		}
		return s
	}
	plan := func(organization types.String) tfsdk.Plan {
		return tfsdk.Plan{Schema: schema, Raw: state(organization, types.StringUnknown()).Raw}
	}
	for name, tc := range map[string]struct {
		planned     types.String
		wantReplace bool
		wantError   string
	}{
		"same organization under its new name": {planned: types.StringValue("team-renamed")},
		"another organization":                 {planned: types.StringValue("team-b"), wantReplace: true},
		"a name that is not known yet":         {planned: types.StringUnknown()},
		"a name no organization has":           {planned: types.StringValue("nobody")},
	} {
		t.Run(name, func(t *testing.T) {
			response := resource.ModifyPlanResponse{Plan: plan(tc.planned)}
			r.ModifyPlan(t.Context(), resource.ModifyPlanRequest{
				State: state(types.StringValue("team-a"), types.StringValue("org-team-a")), Plan: plan(tc.planned),
			}, &response)
			if tc.wantError != "" {
				if !response.Diagnostics.HasError() || !strings.Contains(fmt.Sprint(response.Diagnostics), tc.wantError) {
					t.Fatalf("diagnostics = %v, want %q", response.Diagnostics, tc.wantError)
				}
				return
			}
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			if replaced := len(response.RequiresReplace) > 0; replaced != tc.wantReplace {
				t.Fatalf("replace = %v, want %v", replaced, tc.wantReplace)
			}
		})
	}
}

// The organization of a project can be renamed in the same apply that renames the
// organization resource. At plan the instance still knows the old name only.
func TestLifecycleProjectFollowsAManagedOrganizationRename(t *testing.T) {
	api := newOrganizationInstance(t)
	config := func(name string) string {
		return providerBlock(api, "") +
			fmt.Sprintf("resource \"logfire_organization\" \"team\" {\nname = %q\ndeletion_protection = false\n}\n", name) +
			projectBlock("svc", "organization = logfire_organization.team.name\nname = \"svc\"")
	}
	const address = "logfire_project.svc"
	var projectID string
	runProjectLifecycle(t, []tfresource.TestStep{
		{Config: config("team-a"), Check: func(s *terraform.State) error {
			projectID = s.RootModule().Resources[address].Primary.ID
			return nil
		}},
		{
			Config:           config("team-z"),
			ConfigPlanChecks: expectPlan(address, plancheck.ResourceActionUpdate),
			Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr(address, "organization", "team-z"),
				func(s *terraform.State) error {
					if got := s.RootModule().Resources[address].Primary.ID; got != projectID {
						return fmt.Errorf("project ID = %s, want %s", got, projectID)
					}
					return nil
				},
			),
		},
		{Config: config("team-z"), ConfigPlanChecks: expectNoChange},
	})
	if api.creates != 1 {
		t.Errorf("creates = %d, want the rename to keep the project", api.creates)
	}
}

func TestProjectImportReportsTheFailureOfTheTargetOrganization(t *testing.T) {
	t.Run("a key without instance:admin cannot reach another organization", func(t *testing.T) {
		api := newOrganizationInstance(t, "team-b")
		api.exchangeNeedsAdmin = true
		runProjectLifecycle(t, []tfresource.TestStep{{
			Config:        providerBlock(api, "") + projectBlock("svc", "name = \"svc\""),
			ResourceName:  "logfire_project.svc",
			ImportState:   true,
			ImportStateId: "team-b/svc",
			ExpectError:   regexp.MustCompile(`"team-b"\s+is\s+another\s+organization,\s+an\s+instance-admin\s+key\s+is\s+needed`),
		}})
	})
	t.Run("a project missing from the key's own organization is not blamed on the key", func(t *testing.T) {
		api := newOrganizationInstance(t)
		api.exchangeNeedsAdmin = true
		runProjectLifecycle(t, []tfresource.TestStep{{
			Config:        providerBlock(api, "") + projectBlock("svc", "name = \"svc\""),
			ResourceName:  "logfire_project.svc",
			ImportState:   true,
			ImportStateId: "admin/missing",
			ExpectError: regexp.MustCompile(
				`(?s)project\s+"missing"\s+was\s+not\s+found\s+with\s+this\s+key;\s+if\s+"admin"\s+is\s+another\s+organization,\s+an\s+instance-admin\s+key\s+is\s+needed`),
		}})
	})
	t.Run("a shorthand import keeps the own list's error", func(t *testing.T) {
		api := newOrganizationInstance(t, "team-b")
		api.exchangeNeedsAdmin = true
		api.projectListRefused = true
		runProjectLifecycle(t, []tfresource.TestStep{{
			Config:        providerBlock(api, "") + projectBlock("svc", "name = \"svc\""),
			ResourceName:  "logfire_project.svc",
			ImportState:   true,
			ImportStateId: "team-b/svc",
			ExpectError:   regexp.MustCompile(`(?s)project:read.*instance-admin`),
		}})
	})
	t.Run("a bare import reports a failed project list", func(t *testing.T) {
		api := newOrganizationInstance(t, "team-b")
		api.projectListRefused = true
		runProjectLifecycle(t, []tfresource.TestStep{{
			Config:        providerBlock(api, "team-b") + projectBlock("svc", "name = \"svc\""),
			ResourceName:  "logfire_project.svc",
			ImportState:   true,
			ImportStateId: "svc",
			ExpectError:   regexp.MustCompile(`project:read`),
		}})
	})
}
