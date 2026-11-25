// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

func TestSelectActiveFrontendToken(t *testing.T) {
	t.Parallel()
	oldSecret := "old"
	newSecret := "new"
	tokens := []logclient.FrontendApplicationToken{
		{ID: "new", Status: "active", Token: &newSecret},
		{ID: "old", Status: "active", Token: &oldSecret},
	}
	if got := selectActiveFrontendToken(tokens, "old"); got == nil || got.ID != "old" {
		t.Fatalf("preferred active token = %+v", got)
	}
	tokens[1].Status = "revoked"
	tokens[1].Token = nil
	if got := selectActiveFrontendToken(tokens, "old"); got == nil || got.ID != "new" {
		t.Fatalf("fallback active token = %+v", got)
	}
	if got := selectActiveFrontendToken(tokens[1:], "old"); got != nil {
		t.Fatalf("expected no active token, got %+v", got)
	}
}

func TestShouldRevokeFrontendApplicationToken(t *testing.T) {
	t.Parallel()
	if shouldRevokeFrontendApplicationToken(types.BoolValue(false)) {
		t.Fatal("revoke_on_destroy=false must skip token revocation")
	}
	for _, value := range []types.Bool{types.BoolValue(true), types.BoolNull(), types.BoolUnknown()} {
		if !shouldRevokeFrontendApplicationToken(value) {
			t.Fatalf("revoke_on_destroy=%v must revoke", value)
		}
	}
}

func TestSameNameIdentityReplacementNeedsAdoption(t *testing.T) {
	t.Parallel()
	state := FrontendApplicationModel{
		ProjectID: types.StringValue("project-1"), Name: types.StringValue("browser"),
		ServiceNamespace: types.StringValue("old"), Environment: types.StringValue("prod"),
	}
	plan := state
	plan.ServiceNamespace = types.StringValue("new")
	if !sameNameIdentityReplacementNeedsAdoption(state, plan, nil) {
		t.Fatal("same-name namespace replacement must require explicit adoption")
	}
	plan.AdoptExistingServiceName = types.BoolValue(true)
	if sameNameIdentityReplacementNeedsAdoption(state, plan, nil) {
		t.Fatal("explicit adoption must allow same-name namespace replacement")
	}
	plan.AdoptExistingServiceName = types.BoolValue(false)
	plan.Name = types.StringValue("other")
	if sameNameIdentityReplacementNeedsAdoption(state, plan, nil) {
		t.Fatal("a different name must retain the API collision guard")
	}
	if !sameNameIdentityReplacementNeedsAdoption(state, plan, serverNameValue("other")) {
		t.Fatal("the name Logfire has after a UI rename must require explicit adoption")
	}
	plan.Name = state.Name
	plan.ProjectID = types.StringValue("other-project")
	if sameNameIdentityReplacementNeedsAdoption(state, plan, serverNameValue("other")) {
		t.Fatal("a move to another project cannot collide, so it must not require adoption")
	}
}

const (
	frontendAppAddr     = "logfire_frontend_application.test"
	frontendCurrentAddr = "logfire_frontend_application_token.current"
	frontendReplaceAddr = "logfire_frontend_application_token.replacement"
)

func expectFrontendValue(address, attribute string, value knownvalue.Check) statecheck.StateCheck {
	return statecheck.ExpectKnownValue(address, tfjsonpath.New(attribute), value)
}

// frontendImportID joins the named attributes of a resource in state into an import ID.
func frontendImportID(address string, attributes ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		resourceState, ok := s.RootModule().Resources[address]
		if !ok || resourceState.Primary == nil {
			return "", fmt.Errorf("resource state not found in root module")
		}
		parts := []string{}
		for _, attribute := range attributes {
			parts = append(parts, resourceState.Primary.Attributes[attribute])
		}
		return strings.Join(parts, "/"), nil
	}
}

func TestAccFrontendApplicationResource(t *testing.T) {
	requireExperimentalFrontendApplicationAcceptance(t)
	t.Parallel()
	projectName := fmt.Sprintf("acc-frontend-app-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccFrontendApplicationConfig(projectName, true, false, true),
				ConfigStateChecks: []statecheck.StateCheck{
					expectFrontendValue(frontendAppAddr, "token", knownvalue.NotNull()),
					expectFrontendValue(frontendCurrentAddr, "token", knownvalue.NotNull()),
				},
			},
			{
				ImportState: true, ImportStateVerify: true,
				ResourceName: frontendAppAddr, ImportStateVerifyIgnore: []string{"adopt_existing_service_name"},
				ImportStateIdFunc: frontendImportID(frontendAppAddr, "project_id", "id"),
			},
			{
				Config: testAccFrontendApplicationConfig(projectName, true, true, true),
				ConfigStateChecks: []statecheck.StateCheck{
					expectFrontendValue(frontendCurrentAddr, "token", knownvalue.NotNull()),
					expectFrontendValue(frontendReplaceAddr, "token", knownvalue.NotNull()),
				},
			},
			{
				Config: testAccFrontendApplicationConfig(projectName, false, true, true),
				ConfigStateChecks: []statecheck.StateCheck{
					expectFrontendValue(frontendReplaceAddr, "status", knownvalue.StringExact("active")),
					expectFrontendValue(frontendAppAddr, "token_id", knownvalue.NotNull()),
				},
			},
			{
				ResourceName: frontendReplaceAddr, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: frontendImportID(frontendReplaceAddr, "project_id", "application_id", "id"),
			},
			{
				Config: testAccFrontendApplicationConfig(projectName, false, true, false),
				ConfigStateChecks: []statecheck.StateCheck{
					expectFrontendValue(frontendReplaceAddr, "revoke_on_destroy", knownvalue.Bool(false)),
				},
			},
		},
	})
}

func TestAccFrontendApplicationNamespaceReplacement(t *testing.T) {
	requireExperimentalFrontendApplicationAcceptance(t)
	t.Parallel()
	projectName := fmt.Sprintf("acc-frontend-app-ns-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccFrontendApplicationNamespaceReplacementConfig(projectName, "old", false),
				Check:  resource.TestCheckResourceAttr(frontendAppAddr, "service_namespace", "old"),
			},
			{
				Config: testAccFrontendApplicationNamespaceReplacementConfig(projectName, "new", true),
				Check:  resource.TestCheckResourceAttr(frontendAppAddr, "service_namespace", "new"),
			},
		},
	})
}

func requireExperimentalFrontendApplicationAcceptance(t *testing.T) {
	t.Helper()
	if os.Getenv("LOGFIRE_EXPERIMENTAL_FRONTEND_APPLICATIONS_ACC") == "" {
		t.Skip("set LOGFIRE_EXPERIMENTAL_FRONTEND_APPLICATIONS_ACC=1 to test the staging-only frontend application API")
	}
}

func testAccFrontendApplicationNamespaceReplacementConfig(projectName, namespace string, adopt bool) string {
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name = %q
}

resource "logfire_frontend_application" "test" {
  project_id                  = logfire_project.test.id
  name                        = "browser"
  service_namespace           = %q
  environment                 = "test"
  adopt_existing_service_name = %t
}`, testAccProviderConfig(), projectName, namespace, adopt)
}

func testAccFrontendApplicationConfig(projectName string, includeCurrent, includeReplacement, replacementRevokeOnDestroy bool) string {
	current := ""
	if includeCurrent {
		current = `
resource "logfire_frontend_application_token" "current" {
  project_id     = logfire_project.test.id
  application_id = logfire_frontend_application.test.id
  adopt_token_id = logfire_frontend_application.test.token_id
}
`
	}
	replacement := ""
	if includeReplacement {
		replacement = fmt.Sprintf(`
resource "logfire_frontend_application_token" "replacement" {
  project_id        = logfire_project.test.id
  application_id    = logfire_frontend_application.test.id
  revoke_on_destroy = %t
}
`, replacementRevokeOnDestroy)
	}
	return fmt.Sprintf(`%s

resource "logfire_project" "test" {
  name        = %q
  description = "Acceptance test project for frontend applications"
}

resource "logfire_frontend_application" "test" {
  project_id        = logfire_project.test.id
  name              = "browser"
  service_namespace = "acceptance"
  environment       = "test"

  lifecycle {
    create_before_destroy = true
  }
}
%s%s`, testAccProviderConfig(), projectName, current, replacement)
}
