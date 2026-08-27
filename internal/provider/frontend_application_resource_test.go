// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
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
		Name: types.StringValue("browser"), ServiceNamespace: types.StringValue("old"), Environment: types.StringValue("prod"),
	}
	plan := state
	plan.ServiceNamespace = types.StringValue("new")
	if !sameNameIdentityReplacementNeedsAdoption(state, plan) {
		t.Fatal("same-name namespace replacement must require explicit adoption")
	}
	plan.AdoptExistingServiceName = types.BoolValue(true)
	if sameNameIdentityReplacementNeedsAdoption(state, plan) {
		t.Fatal("explicit adoption must allow same-name namespace replacement")
	}
	plan.AdoptExistingServiceName = types.BoolValue(false)
	plan.Name = types.StringValue("other")
	if sameNameIdentityReplacementNeedsAdoption(state, plan) {
		t.Fatal("a different name must retain the API collision guard")
	}
}

func TestAccFrontendApplicationResource(t *testing.T) {
	t.Parallel()
	projectName := fmt.Sprintf("acc-frontend-app-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccFrontendApplicationConfig(projectName, true, false, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_frontend_application.test", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_frontend_application_token.current", tfjsonpath.New("token"), knownvalue.NotNull()),
				},
			},
			{
				ImportState: true, ImportStateVerify: true,
				ResourceName: "logfire_frontend_application.test", ImportStateVerifyIgnore: []string{"adopt_existing_service_name"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_frontend_application.test"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					return fmt.Sprintf("%s/%s", resourceState.Primary.Attributes["project_id"], resourceState.Primary.Attributes["id"]), nil
				},
			},
			{
				Config: testAccFrontendApplicationConfig(projectName, true, true, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_frontend_application_token.current", tfjsonpath.New("token"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("logfire_frontend_application_token.replacement", tfjsonpath.New("token"), knownvalue.NotNull()),
				},
			},
			{
				Config: testAccFrontendApplicationConfig(projectName, false, true, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_frontend_application_token.replacement", tfjsonpath.New("status"), knownvalue.StringExact("active")),
					statecheck.ExpectKnownValue("logfire_frontend_application.test", tfjsonpath.New("token_id"), knownvalue.NotNull()),
				},
			},
			{
				ResourceName: "logfire_frontend_application_token.replacement", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					resourceState, ok := s.RootModule().Resources["logfire_frontend_application_token.replacement"]
					if !ok || resourceState.Primary == nil {
						return "", fmt.Errorf("resource state not found in root module")
					}
					return fmt.Sprintf("%s/%s/%s", resourceState.Primary.Attributes["project_id"], resourceState.Primary.Attributes["application_id"], resourceState.Primary.Attributes["id"]), nil
				},
			},
			{
				Config: testAccFrontendApplicationConfig(projectName, false, true, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("logfire_frontend_application_token.replacement", tfjsonpath.New("revoke_on_destroy"), knownvalue.Bool(false)),
				},
			},
		},
	})
}

func TestAccFrontendApplicationNamespaceReplacement(t *testing.T) {
	t.Parallel()
	projectName := fmt.Sprintf("acc-frontend-app-ns-%s", acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum))

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccFrontendApplicationNamespaceReplacementConfig(projectName, "old", false),
				Check:  resource.TestCheckResourceAttr("logfire_frontend_application.test", "service_namespace", "old"),
			},
			{
				Config: testAccFrontendApplicationNamespaceReplacementConfig(projectName, "new", true),
				Check:  resource.TestCheckResourceAttr("logfire_frontend_application.test", "service_namespace", "new"),
			},
		},
	})
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
