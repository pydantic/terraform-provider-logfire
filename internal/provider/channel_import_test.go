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

type channelListTransport struct {
	body string
}

func (t channelListTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.Path != "/api/v1/channels/" {
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

func newChannelImportResponse(t *testing.T) resource.ImportStateResponse {
	t.Helper()
	r := &ChannelResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	if schemaResponse.Diagnostics.HasError() {
		t.Fatal(schemaResponse.Diagnostics)
	}
	return resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResponse.Schema, Raw: tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(t.Context()), nil)},
	}
}

func TestChannelImportByUUID(t *testing.T) {
	t.Parallel()
	// A UUID import must resolve without any HTTP call.
	r := &ChannelResource{}
	response := newChannelImportResponse(t)
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
}

func TestChannelImportByLabel(t *testing.T) {
	t.Parallel()
	list := `[
		{"id":"11111111-1111-1111-1111-111111111111","organization_id":"22222222-2222-2222-2222-222222222222","label":"opsgenie-channel","active":true,"created_at":"2026-01-01T00:00:00Z","config":{"type":"opsgenie"}},
		{"id":"9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff","organization_id":"22222222-2222-2222-2222-222222222222","label":"alerts-webhook","active":true,"created_at":"2026-01-02T00:00:00Z","config":{"type":"webhook","format":"auto","url":"https://example.com/hook"}}
	]`
	c, err := logclient.NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: channelListTransport{body: list}})
	if err != nil {
		t.Fatal(err)
	}
	r := &ChannelResource{client: c}
	response := newChannelImportResponse(t)
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: " alerts-webhook "}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
}

func TestChannelImportByLabelNotFound(t *testing.T) {
	t.Parallel()
	c, err := logclient.NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: channelListTransport{body: `[]`}})
	if err != nil {
		t.Fatal(err)
	}
	r := &ChannelResource{client: c}
	response := newChannelImportResponse(t)
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: "missing-channel"}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("expected an import error for an unknown channel label")
	}
}
