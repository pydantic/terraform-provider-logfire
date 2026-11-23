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

// emptyOrganization answers the token endpoint and finds nothing: no channel,
// no schedule, no project.
type emptyOrganization struct{}

func (emptyOrganization) RoundTrip(req *http.Request) (*http.Response, error) {
	status, body := http.StatusOK, `[]`
	switch {
	case req.URL.Path == "/api/oauth/token":
		body = `{"access_token":"org-token","expires_in":900}`
	case strings.Contains(req.URL.Path, "schedules") || strings.Contains(req.URL.Path, "notification-windows"):
		status, body = http.StatusNotFound, `{"detail":"Schedule not found"}`
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func importScopeClient(t *testing.T, organization string) *logclient.APIClient {
	t.Helper()
	c, err := logclient.NewAPIClient("https://logfire.example.invalid", "admin-key", &http.Client{Transport: emptyOrganization{}})
	if err != nil {
		t.Fatal(err)
	}
	return c.ForOrganization(organization)
}

// A not-found answer names the organization that the import searched. With a
// provider organization that is the organization of the provider, not the one
// of the credential.
func TestImportNotFoundNamesTheSearchedOrganization(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		organization string
		want         string
	}{
		"credential's organization": {organization: "", want: "the organization of the API key"},
		"provider organization":     {organization: "team-b", want: `organization "team-b"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := importScopeClient(t, tc.organization)

			channel := &ChannelResource{client: c}
			channelSchema := resource.SchemaResponse{}
			channel.Schema(t.Context(), resource.SchemaRequest{}, &channelSchema)
			channelResponse := resource.ImportStateResponse{State: tfsdk.State{
				Schema: channelSchema.Schema, Raw: tftypes.NewValue(channelSchema.Schema.Type().TerraformType(t.Context()), nil),
			}}
			channel.ImportState(t.Context(), resource.ImportStateRequest{ID: "missing"}, &channelResponse)
			assertDiagnosticsContain(t, channelResponse.Diagnostics, tc.want)

			schedule := &ScheduleResource{client: c}
			scheduleSchema := resource.SchemaResponse{}
			schedule.Schema(t.Context(), resource.SchemaRequest{}, &scheduleSchema)
			scheduleResponse := resource.ImportStateResponse{State: tfsdk.State{
				Schema: scheduleSchema.Schema, Raw: tftypes.NewValue(scheduleSchema.Schema.Type().TerraformType(t.Context()), nil),
			}}
			schedule.ImportState(t.Context(), resource.ImportStateRequest{ID: "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"}, &scheduleResponse)
			assertDiagnosticsContain(t, scheduleResponse.Diagnostics, tc.want)

			project := &ProjectResource{client: c}
			projectResponse := importProject(t, project, "missing")
			assertDiagnosticsContain(t, projectResponse.Diagnostics, tc.want)
		})
	}
}

// A provider that names an organization asked for the exchange. When the key
// cannot exchange, an import by project UUID reports that instead of a miss.
func TestProjectImportByIDReportsExchangeFailure(t *testing.T) {
	t.Parallel()
	r := newProjectTestResource(t, projectInstance{exchangeFailure: exchangeNeedsInstanceAdmin}, "team-b")
	response := importProject(t, r, "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff")
	assertDiagnosticsContain(t, response.Diagnostics, "instance-admin API key")
	if strings.Contains(diagnosticsString(response.Diagnostics), "not found") {
		t.Fatalf("the import hid the exchange failure behind a miss: %s", diagnosticsString(response.Diagnostics))
	}
}

const exchangeRejectsAudience = `{"error":"invalid_target","error_description":"audience does not match the configured OIDC base URL"}`

// An instance-admin key can list the organizations, so an organization that
// exists with a rejected audience shows the mismatch. The organization shorthand
// of an organization that does not exist stays a miss.
func TestProjectImportByOrganizationReportsRejectedAudience(t *testing.T) {
	t.Parallel()
	t.Run("organization exists", func(t *testing.T) {
		t.Parallel()
		r := newProjectTestResource(t, projectInstance{exchangeFailure: exchangeRejectsAudience, organizations: organizationsWithTeamA}, "")
		response := importProject(t, r, "team-a/svc")
		assertDiagnosticsContain(t, response.Diagnostics, "base_url")
	})
	t.Run("organization does not exist", func(t *testing.T) {
		t.Parallel()
		r := newProjectTestResource(t, projectInstance{exchangeFailure: exchangeRejectsAudience, organizations: organizationsWithoutTeamA}, "")
		response := importProject(t, r, "team-a/svc")
		assertDiagnosticsContain(t, response.Diagnostics, `project "team-a"/"svc" not found`)
	})
}
