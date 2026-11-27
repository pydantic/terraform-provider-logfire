// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	guardrailConnectionResource = "logfire_gateway_guardrail_connection.test"
	presidioTestToken           = "synthetic-presidio-token"
	presidioRotatedToken        = "rotated-presidio-token"
)

func guardrailConnectionConfig(baseURL, body string) string {
	return lifecycleConfig(baseURL, "gateway_guardrail_connection", body)
}

// expectConnectionPatch checks the last PATCH body. A nil want means that no PATCH carried `auth`.
func expectConnectionPatch(api *guardrailAPI, wantAuth map[string]any, wantKeys ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id := s.RootModule().Resources[guardrailConnectionResource].Primary.ID
		body := api.lastBody("PATCH " + guardrailConnectionsURL + id + "/")
		if body == nil {
			return fmt.Errorf("no PATCH was sent")
		}
		keys := []string{}
		for key := range body {
			keys = append(keys, key)
		}
		for _, key := range wantKeys {
			if _, ok := body[key]; !ok {
				return fmt.Errorf("PATCH body %v is missing %q", body, key)
			}
		}
		auth, sent := body["auth"]
		if wantAuth == nil && sent {
			return fmt.Errorf("PATCH body %v sent auth without an auth or address change", body)
		}
		if wantAuth != nil && !reflect.DeepEqual(auth, wantAuth) {
			return fmt.Errorf("PATCH auth = %v, want %v (keys %v)", auth, wantAuth, keys)
		}
		return nil
	}
}

func TestLifecycleGatewayGuardrailConnection(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	bearer := func(name, baseURL, token, extra string) string {
		return guardrailConnectionConfig(server.URL, fmt.Sprintf(
			"name = %q\nbase_url = %q\nauth_type = \"bearer\"\nbearer_token = %q\n%s", name, baseURL, token, extra))
	}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: bearer("presidio", "https://presidio.example.com", presidioTestToken, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectSensitiveValue(guardrailConnectionResource, tfjsonpath.New("bearer_token")),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet(guardrailConnectionResource, "id"),
					resource.TestCheckResourceAttr(guardrailConnectionResource, "auth_type", "bearer"),
					resource.TestCheckResourceAttr(guardrailConnectionResource, "timeout_ms", "2000"),
					resource.TestCheckResourceAttr(guardrailConnectionResource, "enabled", "true"),
					resource.TestCheckResourceAttr(guardrailConnectionResource, "guardrail_count", "0"),
					func(*terraform.State) error {
						body := api.lastBody("POST " + guardrailConnectionsURL)
						want := map[string]any{"type": "bearer", "token": presidioTestToken}
						if !reflect.DeepEqual(body["auth"], want) || body["provider"] != "presidio" {
							return fmt.Errorf("create body = %v", body)
						}
						return nil
					},
				),
			},
			{
				// A rename and a timeout change keep the stored token, so the PATCH has no auth.
				Config: bearer("presidio-eu", "https://presidio.example.com", presidioTestToken, "timeout_ms = 3000"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(guardrailConnectionResource, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(guardrailConnectionResource, "name", "presidio-eu"),
					expectConnectionPatch(api, nil, "name", "timeout_ms"),
				),
			},
			{
				Config: bearer("presidio-eu", "https://presidio.example.com", presidioRotatedToken, "timeout_ms = 3000"),
				Check:  expectConnectionPatch(api, map[string]any{"type": "bearer", "token": presidioRotatedToken}),
			},
			{
				// A new address always carries the token, because Logfire never moves a stored token.
				Config: bearer("presidio-eu", "https://presidio-eu.example.com", presidioRotatedToken, "timeout_ms = 3000"),
				Check:  expectConnectionPatch(api, map[string]any{"type": "bearer", "token": presidioRotatedToken}, "base_url"),
			},
			{
				Config:       bearer("presidio-eu", "https://presidio-eu.example.com", presidioRotatedToken, "timeout_ms = 3000"),
				ResourceName: guardrailConnectionResource, ImportState: true, ImportStateId: "presidio-eu",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"bearer_token"},
			},
			{
				Config:       bearer("presidio-eu", "https://presidio-eu.example.com", presidioRotatedToken, "timeout_ms = 3000"),
				ResourceName: guardrailConnectionResource, ImportState: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources[guardrailConnectionResource].Primary.ID, nil
				},
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"bearer_token"},
			},
			{
				Config: guardrailConnectionConfig(server.URL,
					"name = \"presidio-eu\"\nbase_url = \"https://presidio-eu.example.com\"\ntimeout_ms = 3000"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(guardrailConnectionResource, "auth_type", "none"),
					resource.TestCheckNoResourceAttr(guardrailConnectionResource, "bearer_token"),
					expectConnectionPatch(api, map[string]any{"type": "none"}),
				),
			},
			{
				// A connection deleted outside Terraform leaves state on the next refresh.
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					clear(api.connections)
				},
				Config: guardrailConnectionConfig(server.URL,
					"name = \"presidio-eu\"\nbase_url = \"https://presidio-eu.example.com\"\ntimeout_ms = 3000"),
				PlanOnly: true, ExpectNonEmptyPlan: true,
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if api.requests["POST "+guardrailConnectionsURL] != 1 {
		t.Errorf("POST requests = %d, want 1", api.requests["POST "+guardrailConnectionsURL])
	}
	for route, bodies := range api.bodies {
		if strings.HasPrefix(route, "PATCH ") && len(bodies) != 4 {
			t.Errorf("%s requests = %d, want 4", route, len(bodies))
		}
	}
}

func TestLifecycleGatewayGuardrailConnectionHidesToken(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	api.echoToken = true
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: guardrailConnectionConfig(server.URL, fmt.Sprintf(
				"name = \"presidio\"\nbase_url = \"https://presidio.example.com\"\nauth_type = \"bearer\"\nbearer_token = %q",
				presidioTestToken)),
			ExpectError: regexp.MustCompile(`Authorization: Bearer <redacted>`),
		}},
	})
}

func TestGatewayGuardrailConnectionErrorsOmitToken(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	api.echoToken = true
	c, err := logclient.NewAPIClient(server.URL, "test-token", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	token := `quoted"` + presidioTestToken
	auth := logclient.GatewayGuardrailConnectionAuth{Type: "bearer", Token: &token}
	_, createErr := c.CreateGatewayGuardrailConnection(context.Background(), logclient.GatewayGuardrailConnectionCreate{
		Name: "presidio", Provider: "presidio", BaseURL: "https://presidio.example.com", Auth: auth, TimeoutMS: 2000,
	})
	_, updateErr := c.UpdateGatewayGuardrailConnection(context.Background(), "018f45c0-3cab-7b2f-a8f7-000000000001",
		logclient.GatewayGuardrailConnectionUpdate{Auth: &auth})
	for _, err := range []error{createErr, updateErr} {
		if err == nil || !strings.Contains(err.Error(), "<redacted>") || strings.Contains(err.Error(), presidioTestToken) {
			t.Fatalf("error must name the failure without the token: %v", err)
		}
	}
}

func TestLifecycleGatewayGuardrailsOnOlderLogfire(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	api.routesMissing = true
	targets := "targets = [{ action = \"observe\" }]"
	for kind, body := range map[string]string{
		"gateway_guardrail_connection": "name = \"presidio\"\nbase_url = \"https://presidio.example.com\"",
		"gateway_prebuilt_guardrail":   "template_slug = \"email-addresses\"\n" + targets,
		"gateway_regex_guardrail":      "name = \"order-ids\"\npattern = \"ORD-[0-9]+\"\n" + targets,
		"gateway_presidio_guardrail": "name = \"emails\"\nconnection_id = \"018f45c0-3cab-7b2f-a8f7-000000000001\"\n" +
			"on_error = \"allow\"\nentity_type = \"EMAIL_ADDRESS\"\nlanguage = \"en\"\n" + targets,
	} {
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      lifecycleConfig(server.URL, kind, body),
				ExpectError: regexp.MustCompile(`requires\s+Logfire\s+v2026-11-27\.01\s+or\s+newer`),
			}},
		})
	}
}

func TestLifecycleGatewayGuardrailConnectionInUse(t *testing.T) {
	t.Parallel()
	server, api := newGuardrailAPI(t)
	connection := guardrailConnectionConfig(server.URL, "name = \"presidio\"\nbase_url = \"https://presidio.example.com\"")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: connection},
			{
				// A guardrail made outside Terraform uses the connection, so Logfire refuses the delete.
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					for id := range api.connections {
						api.presidio["outside"] = &logclient.GatewayPresidioGuardrailRead{ID: "outside", ConnectionID: id}
					}
				},
				Config:      lifecycleProvider(server.URL),
				ExpectError: regexp.MustCompile(`Delete\s+the\s+Presidio\s+guardrails\s+that\s+use\s+this\s+connection`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					clear(api.presidio)
				},
				Config: lifecycleProvider(server.URL),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if len(api.connections) != 0 {
		t.Fatalf("destroy left connections %v", api.connections)
	}
}
