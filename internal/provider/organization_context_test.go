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

const (
	organizationExchangeInvalidTarget = `{"error":"invalid_target","error_description":"unknown organization: 'acme'"}`
	organizationTestID                = "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"
)

// organizationJSON is one organization as the instance organization list and
// the organization context routes return it.
func organizationJSON(id, name string) string {
	return fmt.Sprintf(`{"id":%q,"organization_name":%q,
	"subscription_plan":"non_stripe","has_admin_panel":false,"created_at":"2026-01-01T00:00:00Z",
	"updated_at":"2026-01-01T00:00:00Z","organization_display_name":null,
	"github_handle":null,"location":null,"avatar":null,"links":[],"description":null,
	"spending_cap":null,"spending_cap_reached_at":null,"planless_grace_period_ends_at":null,
	"gateway_enabled":false,"ai_enabled":false}`, id, name)
}

type organizationTestTransport struct {
	exchangeBody string
	listBody     string
	contextBody  string
}

func (t organizationTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
	switch {
	case req.URL.Path == "/api/oauth/token":
		if t.exchangeBody == "" {
			// A successful exchange by default, as an RFC 8693 exchange response.
			return respond(http.StatusOK, `{"access_token":"exchanged-token","token_type":"Bearer","expires_in":900,
	"scope":"organization:read organization:write","issued_token_type":"urn:ietf:params:oauth:token-type:access_token"}`)
		}
		return respond(http.StatusBadRequest, t.exchangeBody)
	case req.Method == http.MethodGet && req.URL.Path == "/api/v1/organization/":
		if t.contextBody == "" {
			return nil, fmt.Errorf("unexpected org-context request without a contextBody")
		}
		return respond(http.StatusOK, t.contextBody)
	case req.Method == http.MethodGet && req.URL.Path == "/api/v1/instance/organizations/":
		return respond(http.StatusOK, t.listBody)
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
}

func newOrganizationTestResource(t *testing.T, transport organizationTestTransport) *OrganizationResource {
	t.Helper()
	c, err := logclient.NewAPIClient("https://example.invalid", "admin-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return &OrganizationResource{configured{c}}
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
	attributes["id"] = tftypes.NewValue(tftypes.String, organizationTestID)
	attributes["name"] = tftypes.NewValue(tftypes.String, "acme")
	attributes["deletion_protection"] = tftypes.NewValue(tftypes.Bool, false)
	raw := tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), attributes)
	return tfsdk.State{Schema: schemaResponse.Schema, Raw: raw}
}

// An invalid_target exchange whose follow-up list confirms the organization is
// absent is already gone: Read removes the resource from state instead of
// erroring, matching the legacy 404 behavior, and Delete succeeds. Every other
// case fails loudly and Read keeps the resource in state. An audience that does
// not match the instance's base URL for an organization that still exists is a
// configuration error. An organization renamed outside Terraform (found by ID
// under a different name) needs a re-import. A reused name belongs to an
// unrelated organization that must be neither adopted nor deleted.
func TestOrganizationReadAndDeleteWhenOrgIsGoneOrChanged(t *testing.T) {
	t.Parallel()

	existing := "[" + organizationJSON(organizationTestID, "acme") + "]"
	renamed := "[" + organizationJSON(organizationTestID, "renamed-elsewhere") + "]"
	tests := []struct {
		name       string
		transport  organizationTestTransport
		wantErr    bool
		skipDelete bool
	}{
		{
			name:      "gone",
			transport: organizationTestTransport{exchangeBody: organizationExchangeInvalidTarget, listBody: "[]"},
		},
		{
			name: "audience mismatch",
			transport: organizationTestTransport{
				exchangeBody: organizationExchangeInvalidTarget,
				listBody:     existing,
			},
			wantErr: true,
		},
		{
			name: "external rename",
			transport: organizationTestTransport{
				exchangeBody: organizationExchangeInvalidTarget,
				listBody:     renamed,
			},
			wantErr:    true,
			skipDelete: true,
		},
		{
			name: "reused name",
			transport: organizationTestTransport{
				contextBody: organizationJSON("33333333-3333-3333-3333-333333333333", "acme"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run("read "+tt.name, func(t *testing.T) {
			t.Parallel()
			r := newOrganizationTestResource(t, tt.transport)
			state := organizationTestState(t)
			response := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
			if response.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diagnostics = %v, want error %v", response.Diagnostics, tt.wantErr)
			}
			if response.State.Raw.IsNull() == tt.wantErr {
				t.Fatalf("state removed = %v, want it removed only for a gone organization", !tt.wantErr)
			}
		})

		if tt.skipDelete {
			continue
		}
		t.Run("delete "+tt.name, func(t *testing.T) {
			t.Parallel()
			r := newOrganizationTestResource(t, tt.transport)
			state := organizationTestState(t)
			response := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
			if response.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diagnostics = %v, want error %v", response.Diagnostics, tt.wantErr)
			}
		})
	}
}

// An import by UUID binds to the organization with that ID even when an
// organization earlier in the list has that UUID as its name.
func TestOrganizationImportPrefersIDMatchOverUUIDShapedName(t *testing.T) {
	t.Parallel()

	list := "[" + organizationJSON("11111111-1111-1111-1111-111111111111", organizationTestID) + "," +
		organizationJSON(organizationTestID, "acme") + "]"
	r := newOrganizationTestResource(t, organizationTestTransport{listBody: list})
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	response := resource.ImportStateResponse{State: tfsdk.State{
		Schema: schemaResponse.Schema,
		Raw:    tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), nil),
	}}
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: organizationTestID}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	var model OrganizationModel
	if diags := response.State.Get(t.Context(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	if model.ID.ValueString() != organizationTestID || model.Name.ValueString() != "acme" {
		t.Fatalf("imported id=%q name=%q, want the ID match (id=9f9b..., name=acme)",
			model.ID.ValueString(), model.Name.ValueString())
	}
}
