// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const frontendApplicationsURL = "/api/v1/projects/project-1/frontend-applications/"

type frontendApplicationAPI struct {
	sync.Mutex
	application *logclient.FrontendApplication
	creates     int
}

func newFrontendApplicationAPI(t *testing.T) (*httptest.Server, *frontendApplicationAPI) {
	t.Helper()
	api := &frontendApplicationAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Lock()
		defer api.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-org-token","expires_in":900}`))
			return
		}
		route := r.Method + " " + strings.TrimPrefix(r.URL.Path, frontendApplicationsURL)
		switch {
		case route == "POST ":
			var in logclient.FrontendApplicationCreate
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("create body: %v", err)
			}
			api.creates++
			api.application = &logclient.FrontendApplication{
				ID: fmt.Sprintf("app-%d", api.creates), ProjectID: "project-1", Name: in.Name, CreatedAt: "2026-11-18T00:00:00Z",
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(logclient.FrontendApplicationCreated{FrontendApplication: *api.application, TokenID: "token-1", Token: "secret"})
		case api.application == nil || !strings.HasPrefix(route[strings.Index(route, " ")+1:], api.application.ID+"/"):
			w.WriteHeader(http.StatusNotFound)
		case route == "GET "+api.application.ID+"/":
			_ = json.NewEncoder(w).Encode(api.application)
		case route == "GET "+api.application.ID+"/tokens/":
			_, _ = w.Write([]byte(`{"data":[{"id":"token-1","status":"active","token":"secret","created_at":"2026-11-18T00:00:00Z"}],"next_cursor":null}`))
		case route == "DELETE "+api.application.ID+"/":
			api.application = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected API request: %s", route)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, api
}

// The Logfire UI can rename an application, but the public API cannot, so a config change replaces it, unless it matches the name the application already has.
func TestLifecycleFrontendApplicationUIRename(t *testing.T) {
	t.Parallel()
	server, api := newFrontendApplicationAPI(t)
	config := func(name string) string {
		return lifecycleConfig(server.URL, "frontend_application", fmt.Sprintf("project_id = \"project-1\"\nname = %q", name))
	}
	const resourceName = "logfire_frontend_application.test"
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("browser"), Check: resource.TestCheckResourceAttr(resourceName, "id", "app-1")},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.application.Name = "renamed-in-ui"
				},
				Config: config("browser"), PlanOnly: true,
			},
			{
				Config: config("browser"), ResourceName: resourceName, ImportState: true, ImportStateId: "project-1/app-1",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if got := states[0].Attributes["name"]; got != "renamed-in-ui" {
						return fmt.Errorf("imported name = %q, want the name from the API", got)
					}
					return nil
				},
			},
			{
				Config: config("renamed-in-ui"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", "app-1"),
					resource.TestCheckResourceAttr(resourceName, "name", "renamed-in-ui"),
				),
			},
			{
				Config: config("other"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr(resourceName, "id", "app-2"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.application != nil {
		t.Fatal("destroy left the application in the API")
	}
}
