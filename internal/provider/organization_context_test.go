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
}

func (t organizationTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.URL.Path == "/api/oauth/token":
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(t.exchangeBody)),
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
	response := resource.DeleteResponse{State: state}
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("delete of an existing organization behind a broken audience must error")
	}
}
