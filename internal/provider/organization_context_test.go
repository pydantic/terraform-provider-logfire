// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
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

type organizationTestTransport struct {
	body string
}

func (t organizationTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
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
// exchange (the audience names no existing organization) removes the
// resource from state instead of erroring, matching the legacy 404 behavior.
func TestOrganizationReadRemovesStateWhenOrgGone(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{body: organizationExchangeInvalidTarget},
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

// TestOrganizationDeleteToleratesGoneOrg verifies delete treats an
// invalid_target exchange as already-gone success.
func TestOrganizationDeleteToleratesGoneOrg(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{
		Transport: organizationTestTransport{body: organizationExchangeInvalidTarget},
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
