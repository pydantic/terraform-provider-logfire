// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	exchangeUnknownOrganization = `{"error":"invalid_target","error_description":"unknown organization: 'team-a'"}`
	exchangeNeedsInstanceAdmin  = `{"error":"invalid_scope","error_description":"subject_token must include instance:admin"}`
	organizationsWithTeamA      = `[{"id":"org-1","organization_name":"team-a"},{"id":"org-2","organization_name":"team-b"}]`
	organizationsWithoutTeamA   = `[{"id":"org-2","organization_name":"team-b"}]`
	organizationsAfterRename    = `[{"id":"org-1","organization_name":"team-z"},{"id":"org-2","organization_name":"team-b"}]`
)

// projectInstance answers the token endpoint, the organization list, and the
// project routes of an instance whose key acts in an organization without
// projects.
type projectInstance struct {
	exchangeFailure string
	// organizations is the body of the organization list. Empty makes the list fail.
	organizations string
	// ownOrganization is the body that the key gets for its own organization.
	// Empty makes the route refuse the key.
	ownOrganization string
}

func (p projectInstance) RoundTrip(req *http.Request) (*http.Response, error) {
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: make(http.Header), Request: req,
			Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	switch {
	case req.URL.Path == "/api/oauth/token":
		if p.exchangeFailure != "" {
			return respond(http.StatusBadRequest, p.exchangeFailure)
		}
		return respond(http.StatusOK, `{"access_token":"org-token","expires_in":900}`)
	case req.URL.Path == "/api/v1/instance/organizations/":
		// The list and the exchange both need instance:admin.
		if p.exchangeFailure == exchangeNeedsInstanceAdmin {
			return respond(http.StatusForbidden, `{"detail":"Missing required scopes: instance:admin"}`)
		}
		if p.organizations == "" {
			return respond(http.StatusInternalServerError, `{"detail":"database unavailable"}`)
		}
		return respond(http.StatusOK, p.organizations)
	case req.URL.Path == "/api/v1/organization/":
		if p.ownOrganization == "" {
			return respond(http.StatusForbidden, `{"detail":"This token is missing the required scope: organization:read."}`)
		}
		return respond(http.StatusOK, p.ownOrganization)
	case req.URL.Path == "/api/v1/projects/" && req.Method == http.MethodGet:
		return respond(http.StatusOK, `[]`)
	case req.Method == http.MethodDelete:
		return respond(http.StatusNoContent, ``)
	default:
		return respond(http.StatusOK, `{"id":"project-1","project_name":"svc","organization_name":"team-a","visibility":"public"}`)
	}
}

func newTestClient(t *testing.T, instance projectInstance) *logclient.APIClient {
	t.Helper()
	c, err := logclient.NewAPIClient("https://logfire.example.invalid", "admin-key", &http.Client{Transport: instance})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newProjectTestResource(t *testing.T, instance projectInstance, providerOrganization string) *ProjectResource {
	t.Helper()
	return &ProjectResource{client: newTestClient(t, instance).ForOrganization(providerOrganization)}
}

func projectSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	var schemaResponse resource.SchemaResponse
	(&ProjectResource{}).Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	if schemaResponse.Diagnostics.HasError() {
		t.Fatal(schemaResponse.Diagnostics)
	}
	return schemaResponse
}

// projectTestState is the state of a project in team-a.
func projectTestState(t *testing.T) tfsdk.State {
	t.Helper()
	schema := projectSchema(t).Schema
	state := tfsdk.State{Schema: schema, Raw: tftypes.NewValue(schema.Type().TerraformType(t.Context()), nil)}
	diags := state.Set(t.Context(), &ProjectModel{
		ID: types.StringValue("project-1"), Organization: types.StringValue("team-a"), Name: types.StringValue("svc"),
		Description: types.StringNull(), Visibility: types.StringValue("public"),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

// readProject reads the project of a state without an organization record:
// one written before the provider kept the record.
func readProject(t *testing.T, r *ProjectResource) resource.ReadResponse {
	t.Helper()
	state := projectTestState(t)
	response := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
	return response
}

func TestProjectReadFailsWhenTheOrganizationStillExists(t *testing.T) {
	t.Parallel()
	r := newProjectTestResource(t, projectInstance{exchangeFailure: exchangeUnknownOrganization, organizations: organizationsWithTeamA}, "team-a")
	response := readProject(t, r)
	assertDiagnosticsContain(t, response.Diagnostics, "base_url")
	if response.State.Raw.IsNull() {
		t.Fatal("a rejected audience must not drop the project from state")
	}
}

// Without a record, the provider cannot tell a deleted organization from a
// renamed one, so it keeps the project and asks.
func TestProjectReadInMissingOrganizationWithoutRecordFails(t *testing.T) {
	t.Parallel()
	r := newProjectTestResource(t, projectInstance{organizations: organizationsWithoutTeamA}, "team-a")
	response := readProject(t, r)
	assertDiagnosticsContain(t, response.Diagnostics, `Organization "team-a" does not exist`)
	if response.State.Raw.IsNull() {
		t.Fatal("an organization the provider cannot place must not drop the project from state")
	}
}

func TestProjectReadFailsWhenTheOrganizationListFails(t *testing.T) {
	t.Parallel()
	r := newProjectTestResource(t, projectInstance{exchangeFailure: exchangeUnknownOrganization}, "team-a")
	response := readProject(t, r)
	assertDiagnosticsContain(t, response.Diagnostics, "list the instance organizations")
	if response.State.Raw.IsNull() {
		t.Fatal("an unconfirmed organization must not drop the project from state")
	}
}

func TestProjectReadNamesInstanceAdminRequirement(t *testing.T) {
	t.Parallel()
	r := newProjectTestResource(t, projectInstance{exchangeFailure: exchangeNeedsInstanceAdmin}, "team-a")
	response := readProject(t, r)
	assertDiagnosticsContain(t, response.Diagnostics, "instance-admin API key")
	if response.State.Raw.IsNull() {
		t.Fatal("a refused exchange must not drop the project from state")
	}
}

func TestProjectDeleteFailsWhenTheOrganizationStillExists(t *testing.T) {
	t.Parallel()
	r := newProjectTestResource(t, projectInstance{exchangeFailure: exchangeUnknownOrganization, organizations: organizationsWithTeamA}, "team-a")
	state := projectTestState(t)
	response := resource.DeleteResponse{State: state}
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
	assertDiagnosticsContain(t, response.Diagnostics, "base_url")
}

func importProject(t *testing.T, r *ProjectResource, id string) resource.ImportStateResponse {
	t.Helper()
	schema := projectSchema(t).Schema
	response := resource.ImportStateResponse{State: tfsdk.State{
		Schema: schema, Raw: tftypes.NewValue(schema.Type().TerraformType(t.Context()), nil),
	}}
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &response)
	return response
}

// A credential that cannot exchange gets the same answer for a project in
// another organization as for a missing one.
func TestProjectImportInAnotherOrganizationWithoutInstanceAdminReportsNotFound(t *testing.T) {
	t.Parallel()
	for name, failure := range map[string]string{"not an instance-admin key": exchangeNeedsInstanceAdmin, "unknown organization": exchangeUnknownOrganization} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			response := importProject(t, newProjectTestResource(t, projectInstance{exchangeFailure: failure}, ""), "team-a/svc")
			assertDiagnosticsContain(t, response.Diagnostics, `project "team-a"/"svc" not found`)
			if strings.Contains(diagnosticsString(response.Diagnostics), "token exchange") {
				t.Fatalf("diagnostics leak the exchange failure: %s", diagnosticsString(response.Diagnostics))
			}
		})
	}
}

// A user who sets the provider organization asked for the exchange, so its
// failure is the answer.
func TestProjectImportWithProviderOrganizationReportsExchangeFailure(t *testing.T) {
	t.Parallel()
	response := importProject(t, newProjectTestResource(t, projectInstance{exchangeFailure: exchangeNeedsInstanceAdmin}, "team-b"), "team-a/svc")
	assertDiagnosticsContain(t, response.Diagnostics, "instance-admin API key")
}

type privateStateStub map[string][]byte

func (s privateStateStub) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	return s[key], nil
}

func (s privateStateStub) SetKey(_ context.Context, key string, value []byte) diag.Diagnostics {
	if len(value) == 0 {
		delete(s, key)
		return nil
	}
	s[key] = value
	return nil
}

func TestClientForOrganization(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, projectInstance{})
	for name, tc := range map[string]struct {
		client       *logclient.APIClient
		organization types.String
		want         string
	}{
		"nothing set":                        {client: c, organization: types.StringNull()},
		"computed value":                     {client: c, organization: types.StringUnknown()},
		"empty value":                        {client: c, organization: types.StringValue("")},
		"configured value":                   {client: c, organization: types.StringValue("team-a"), want: "team-a"},
		"provider organization":              {client: c.ForOrganization("team-b"), organization: types.StringNull(), want: "team-b"},
		"value overrides the provider":       {client: c.ForOrganization("team-b"), organization: types.StringValue("team-a"), want: "team-a"},
		"provider organization, empty value": {client: c.ForOrganization("team-b"), organization: types.StringValue(""), want: "team-b"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := clientForOrganization(tc.client, tc.organization).Organization(); got != tc.want {
				t.Fatalf("create acts in %q, want %q", got, tc.want)
			}
		})
	}
}

// clientForState decides which organization reaches an existing resource.
// The record names the organization by ID, so a rename, a deletion, and a
// provider that now names another organization each give their own answer
// instead of acting on whatever organization the name reaches now.
func TestClientForState(t *testing.T) {
	t.Parallel()
	teamA := `{"id":"org-1","name":"team-a"}`
	ownTeamA := `{"id":"org-1","organization_name":"team-a"}`
	ownTeamB := `{"id":"org-2","organization_name":"team-b"}`
	for name, tc := range map[string]struct {
		organizations string
		listRefused   bool
		own           string
		provider      string
		organization  types.String
		record        string
		want          string
		gone          bool
		wantErr       string
	}{
		"own organization":                                   {organization: types.StringNull()},
		"computed value":                                     {organization: types.StringUnknown()},
		"project reached with the key, no record":            {organization: types.StringValue("team-a")},
		"no record adopts the provider":                      {provider: "team-b", organization: types.StringNull(), want: "team-b"},
		"no record, project in another":                      {provider: "team-b", organization: types.StringValue("team-a"), want: "team-a"},
		"no record, missing organization":                    {provider: "team-c", organization: types.StringNull(), wantErr: `Organization "team-c" does not exist`},
		"record, same provider":                              {provider: "team-a", organization: types.StringNull(), record: teamA, want: "team-a"},
		"record, project with the key":                       {organization: types.StringValue("team-a"), record: teamA, want: "team-a"},
		"record, project under another provider":             {provider: "team-b", organization: types.StringValue("team-a"), record: teamA, want: "team-a"},
		"record, retargeted provider":                        {provider: "team-b", organization: types.StringNull(), record: teamA, wantErr: `lives in organization "team-a", but the provider now acts in "team-b"`},
		"record, provider without organization":              {organization: types.StringNull(), record: teamA, wantErr: "now acts in the organization of its API key"},
		"record, renamed, old name":                          {organizations: organizationsAfterRename, provider: "team-a", organization: types.StringNull(), record: teamA, wantErr: `"team-a" was renamed to "team-z"`},
		"record, renamed, new name":                          {organizations: organizationsAfterRename, provider: "team-z", organization: types.StringNull(), record: teamA, want: "team-z"},
		"record, renamed project":                            {organizations: organizationsAfterRename, organization: types.StringValue("team-a"), record: teamA, want: "team-z"},
		"record, provider renamed ahead of the organization": {provider: "team-z", organization: types.StringNull(), record: teamA, wantErr: "apply the rename alone first"},
		"record, deleted":                                    {organizations: organizationsWithoutTeamA, provider: "team-a", organization: types.StringNull(), record: teamA, gone: true},
		"unreadable record":                                  {provider: "team-a", organization: types.StringNull(), record: "true", wantErr: "not an organization record"},
		"record, key of the recorded organization":           {listRefused: true, own: ownTeamA, organization: types.StringNull(), record: teamA},
		"record, key of the recorded organization, project":  {listRefused: true, own: ownTeamA, organization: types.StringValue("team-a"), record: teamA},
		"record, key of it, instance-admin key":              {own: ownTeamA, organization: types.StringNull(), record: teamA},
		"record, key of another organization":                {listRefused: true, own: ownTeamB, organization: types.StringNull(), record: teamA, wantErr: `lives in organization "team-a", but the provider now acts in the organization of its API key, "team-b"`},
		"record, key that cannot say which":                  {listRefused: true, organization: types.StringNull(), record: teamA, wantErr: "organization:read"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if tc.organizations == "" {
				tc.organizations = organizationsWithTeamA
			}
			instance := projectInstance{organizations: tc.organizations, ownOrganization: tc.own}
			if tc.listRefused {
				instance.exchangeFailure = exchangeNeedsInstanceAdmin
			}
			c := newTestClient(t, instance).ForOrganization(tc.provider)
			private := privateStateStub{}
			if tc.record != "" {
				private[organizationContextKey] = []byte(tc.record)
			}
			got, found, diags := clientForState(t.Context(), c, tc.organization, private)
			if tc.wantErr != "" {
				assertDiagnosticsContain(t, diags, tc.wantErr)
				return
			}
			if diags.HasError() {
				t.Fatal(diagnosticsString(diags))
			}
			if found == tc.gone {
				t.Fatalf("found = %v, want %v", found, !tc.gone)
			}
			if !tc.gone && got.Organization() != tc.want {
				t.Fatalf("existing resource acts in %q, want %q", got.Organization(), tc.want)
			}
		})
	}
}

func TestSaveOrganizationContextRecordsTheOrganizationID(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, projectInstance{organizations: organizationsWithTeamA, ownOrganization: `{"id":"org-2","organization_name":"team-b"}`})
	private := privateStateStub{}
	if diags := saveOrganizationContext(t.Context(), private, c); diags.HasError() {
		t.Fatal(diagnosticsString(diags))
	}
	if got := string(private[organizationContextKey]); got != `{"id":"org-2","name":"team-b"}` {
		t.Fatalf("record of a client of the own organization = %s, want the organization the key reports", got)
	}
	if diags := saveOrganizationContext(t.Context(), private, c.ForOrganization("team-a")); diags.HasError() {
		t.Fatal(diagnosticsString(diags))
	}
	if got := string(private[organizationContextKey]); got != `{"id":"org-1","name":"team-a"}` {
		t.Fatalf("record = %s, want team-a's ID and name", got)
	}
	assertDiagnosticsContain(t, saveOrganizationContext(t.Context(), privateStateStub{}, c.ForOrganization("team-c")),
		`Organization "team-c" does not exist`)
}

// A key that cannot read its own organization has nothing to record. The save
// still succeeds, and a record that exists stays.
func TestSaveOrganizationContextWithoutAnOrganizationReadLeavesStateAlone(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, projectInstance{organizations: organizationsWithTeamA})
	private := privateStateStub{organizationContextKey: []byte(`{"id":"org-2","name":"team-b"}`)}
	if diags := saveOrganizationContext(t.Context(), private, c); diags.HasError() {
		t.Fatal(diagnosticsString(diags))
	}
	if got := string(private[organizationContextKey]); got != `{"id":"org-2","name":"team-b"}` {
		t.Fatalf("record = %s, want it unchanged", got)
	}
	empty := privateStateStub{}
	if diags := saveOrganizationContext(t.Context(), empty, c); diags.HasError() || len(empty) != 0 {
		t.Fatalf("a key without organization:read recorded %q (diagnostics: %s)", empty, diagnosticsString(diags))
	}
}

// A state without a record can belong to a project created with a key of its
// own organization. The exchange reaches it when the key alone cannot, and a
// key that cannot exchange keeps its own answer.
func TestWithExchangeFallback(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, projectInstance{})
	apiError := func(status int) error { return &logclient.APIError{StatusCode: status} }
	for name, tc := range map[string]struct {
		client       *logclient.APIClient
		organization types.String
		direct       error
		exchange     error
		wantClient   string
		wantStatus   int
		wantErr      error
	}{
		"direct success":                         {client: c, organization: types.StringValue("team-a"), wantStatus: 0},
		"forbidden, then found":                  {client: c, organization: types.StringValue("team-a"), direct: apiError(403), wantClient: "team-a"},
		"not found here, found there":            {client: c, organization: types.StringValue("team-a"), direct: apiError(404), wantClient: "team-a"},
		"not found in either":                    {client: c, organization: types.StringValue("team-a"), direct: apiError(404), exchange: apiError(404), wantClient: "team-a", wantStatus: 404},
		"key that cannot exchange keeps its 404": {client: c, organization: types.StringValue("team-a"), direct: apiError(404), exchange: &logclient.InstanceAdminRequiredError{Err: &logclient.OAuthTokenError{Code: "invalid_scope"}}, wantStatus: 404},
		"organization the exchange rejects":      {client: c, organization: types.StringValue("team-a"), direct: apiError(404), exchange: &logclient.OAuthTokenError{Code: "invalid_target"}, wantStatus: 404},
		"exchange times out after a 404":         {client: c, organization: types.StringValue("team-a"), direct: apiError(404), exchange: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		"exchange unavailable after a 404":       {client: c, organization: types.StringValue("team-a"), direct: apiError(404), exchange: &logclient.OAuthTokenError{Code: "temporarily_unavailable"}, wantErr: &logclient.OAuthTokenError{Code: "temporarily_unavailable"}},
		"scoped request fails after a 404":       {client: c, organization: types.StringValue("team-a"), direct: apiError(404), exchange: apiError(503), wantClient: "", wantStatus: 503},
		"exchange times out after a 403":         {client: c, organization: types.StringValue("team-a"), direct: apiError(403), exchange: context.DeadlineExceeded, wantStatus: 403},
		"server error is not retried":            {client: c, organization: types.StringValue("team-a"), direct: apiError(500), exchange: errors.New("called"), wantStatus: 500},
		"no organization in state":               {client: c, organization: types.StringNull(), direct: apiError(403), exchange: errors.New("called"), wantStatus: 403},
		"already through the exchange":           {client: c.ForOrganization("team-a"), organization: types.StringValue("team-a"), direct: apiError(404), exchange: errors.New("called"), wantClient: "team-a", wantStatus: 404},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first := true
			got, err := withExchangeFallback(tc.client, tc.organization, func(c *logclient.APIClient) error {
				if first {
					first = false
					return tc.direct
				}
				return tc.exchange
			})
			if got.Organization() != tc.wantClient {
				t.Fatalf("result came from %q, want %q", got.Organization(), tc.wantClient)
			}
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErr.Error() {
					t.Fatalf("err = %v, want %v, not a missing resource", err, tc.wantErr)
				}
				return
			}
			var apiErr *logclient.APIError
			status := 0
			if errors.As(err, &apiErr) {
				status = apiErr.StatusCode
			} else if err != nil {
				t.Fatalf("err = %v, want an API error", err)
			}
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}
