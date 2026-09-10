// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const organizationExchangeInvalidTarget = `{"error":"invalid_target","error_description":"unknown organization: 'acme'"}`

// exchangeOKBody mirrors a successful RFC 8693 exchange response.
const exchangeOKBody = `{"access_token":"exchanged-token","token_type":"Bearer","expires_in":900,
	"scope":"organization:read organization:write","issued_token_type":"urn:ietf:params:oauth:token-type:access_token"}`

// organizationListBody is one organization matching the test state's name
// and ID, as returned by the instance organization list route.
const organizationListBody = `[{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","organization_name":"acme",
	"subscription_plan":"non_stripe","has_admin_panel":false,"created_at":"2026-01-01T00:00:00Z",
	"updated_at":"2026-01-01T00:00:00Z","billing_email":null,"organization_display_name":null,
	"github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
	"spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
	"gateway_enabled":false,"ai_enabled":false}]`

type organizationTestTransport struct {
	exchangeBody string
	listBody     string
	contextBody  string
}

func (t organizationTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.URL.Path == "/api/oauth/token":
		if t.exchangeBody == "" {
			// A successful exchange by default.
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(exchangeOKBody)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(t.exchangeBody)),
			Request:    req,
		}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/api/v1/organization/":
		if t.contextBody == "" {
			return nil, fmt.Errorf("unexpected org-context request without a contextBody")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(t.contextBody)),
			Request:    req,
		}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/api/v1/instance/organizations/":
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(t.listBody)),
			Request:    req,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
}

// organizationTestState builds a Read/Delete response state holding an
// organization named acme with deletion_protection disabled.
func organizationTestState(t *testing.T) tfsdk.State {
	t.Helper()
	r := &OrganizationResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	if schemaResponse.Diagnostics.HasError() {
		t.Fatal(schemaResponse.Diagnostics)
	}
	attributes := map[string]tftypes.Value{}
	for name, attribute := range schemaResponse.Schema.Attributes {
		attributes[name] = tftypes.NewValue(attribute.GetType().TerraformType(t.Context()), nil)
	}
	attributes["id"] = tftypes.NewValue(tftypes.String, "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff")
	attributes["name"] = tftypes.NewValue(tftypes.String, "acme")
	attributes["deletion_protection"] = tftypes.NewValue(tftypes.Bool, false)
	raw := tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), attributes)
	return tfsdk.State{Schema: schemaResponse.Schema, Raw: raw}
}

// TestOrganizationReadRemovesStateWhenOrgGone verifies that an invalid_target
// exchange whose follow-up list confirms the organization is absent removes
// the resource from state instead of erroring, matching the legacy 404
// behavior.
func TestOrganizationReadRemovesStateWhenOrgGone(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{
			exchangeBody: organizationExchangeInvalidTarget,
			listBody:     "[]",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	state := organizationTestState(t)
	response := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
	if response.Diagnostics.HasError() {
		t.Fatalf("read of a gone organization must not error: %v", response.Diagnostics)
	}
	if !response.State.Raw.IsNull() {
		t.Fatal("read of a gone organization must remove the resource from state")
	}
}

// TestOrganizationReadKeepsStateOnAudienceMismatch verifies that an
// invalid_target exchange for an organization that still exists (audience
// does not match the instance's base URL: a configuration error) fails
// loudly instead of silently dropping the resource from state.
func TestOrganizationReadKeepsStateOnAudienceMismatch(t *testing.T) {
	t.Parallel()
	list := organizationListBody
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{
			exchangeBody: organizationExchangeInvalidTarget,
			listBody:     list,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	state := organizationTestState(t)
	response := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("an audience mismatch for an existing organization must error")
	}
	if response.State.Raw.IsNull() {
		t.Fatal("an audience mismatch must not remove the resource from state")
	}
}

// TestOrganizationDeleteToleratesGoneOrg verifies delete treats an
// invalid_target exchange, confirmed absent by the follow-up list, as
// already-gone success.
func TestOrganizationDeleteToleratesGoneOrg(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{
			exchangeBody: organizationExchangeInvalidTarget,
			listBody:     "[]",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	state := organizationTestState(t)
	response := resource.DeleteResponse{State: state}
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
	if response.Diagnostics.HasError() {
		t.Fatalf("delete of a gone organization must not error: %v", response.Diagnostics)
	}
}

// TestOrganizationDeleteFailsOnAudienceMismatch verifies delete errors when
// the invalid_target exchange targets an organization that still exists.
func TestOrganizationDeleteFailsOnAudienceMismatch(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{
			exchangeBody: organizationExchangeInvalidTarget,
			listBody:     organizationListBody,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	state := organizationTestState(t)
	response := resource.DeleteResponse{State: state}
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("delete of an existing organization behind a broken audience must error")
	}
}

// TestOrganizationReadFailsOnExternalRename verifies that an organization
// renamed outside Terraform (found by ID under a different name) errors with
// a re-import hint instead of dropping the resource from state.
func TestOrganizationReadFailsOnExternalRename(t *testing.T) {
	t.Parallel()
	list := `[{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","organization_name":"renamed-elsewhere",
		"subscription_plan":"non_stripe","has_admin_panel":false,"created_at":"2026-01-01T00:00:00Z",
		"updated_at":"2026-01-01T00:00:00Z","billing_email":null,"organization_display_name":null,
		"github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
		"spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
		"gateway_enabled":false,"ai_enabled":false}]`
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{
			exchangeBody: organizationExchangeInvalidTarget,
			listBody:     list,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	state := organizationTestState(t)
	response := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("an externally renamed organization must error")
	}
	if response.State.Raw.IsNull() {
		t.Fatal("an externally renamed organization must not be removed from state")
	}
}

// organizationReusedNameContextBody is the org-context read result for an
// unrelated organization that inherited the state's name.
const organizationReusedNameContextBody = `{"id":"33333333-3333-3333-3333-333333333333","organization_name":"acme",
	"subscription_plan":"non_stripe","has_admin_panel":false,"created_at":"2026-01-01T00:00:00Z",
	"updated_at":"2026-01-01T00:00:00Z","billing_email":null,"organization_display_name":null,
	"github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
	"spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
	"gateway_enabled":false,"ai_enabled":false}`

// TestOrganizationImportPrefersIDMatchOverUUIDShapedName verifies that an
// import by UUID binds to the organization with that ID even when an
// organization earlier in the list has that UUID as its name.
func TestOrganizationImportPrefersIDMatchOverUUIDShapedName(t *testing.T) {
	t.Parallel()
	list := `[
		{"id":"11111111-1111-1111-1111-111111111111","organization_name":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff",
		 "subscription_plan":"non_stripe","has_admin_panel":false,"created_at":"2026-01-01T00:00:00Z",
		 "updated_at":"2026-01-01T00:00:00Z","billing_email":null,"organization_display_name":null,
		 "github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
		 "spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
		 "gateway_enabled":false,"ai_enabled":false},
		{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","organization_name":"acme",
		 "subscription_plan":"non_stripe","has_admin_panel":false,"created_at":"2026-01-01T00:00:00Z",
		 "updated_at":"2026-01-01T00:00:00Z","billing_email":null,"organization_display_name":null,
		 "github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
		 "spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
		 "gateway_enabled":false,"ai_enabled":false}
	]`
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{listBody: list},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	response := resource.ImportStateResponse{State: tfsdk.State{
		Schema: schemaResponse.Schema,
		Raw:    tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), nil),
	}}
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	var model OrganizationModel
	if diags := response.State.Get(t.Context(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	if model.ID.ValueString() != "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff" || model.Name.ValueString() != "acme" {
		t.Fatalf("imported id=%q name=%q, want the ID match (id=9f9b..., name=acme)",
			model.ID.ValueString(), model.Name.ValueString())
	}
}
func TestOrganizationReadRefusesNameReuse(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{
			contextBody: organizationReusedNameContextBody,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	state := organizationTestState(t)
	response := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("a reused organization name must error instead of being adopted")
	}
	if response.State.Raw.IsNull() {
		t.Fatal("a reused organization name must not remove state")
	}
}

// TestOrganizationDeleteRefusesNameReuse verifies delete refuses to remove an
// unrelated organization that inherited the state's name.
func TestOrganizationDeleteRefusesNameReuse(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{
			contextBody: organizationReusedNameContextBody,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &OrganizationResource{client: c}
	state := organizationTestState(t)
	response := resource.DeleteResponse{State: state}
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("delete of an unrelated organization behind a reused name must error")
	}
}
