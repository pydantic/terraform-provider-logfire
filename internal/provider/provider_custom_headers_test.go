// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestCustomHeadersFromConfigAcceptsValidHeaders(t *testing.T) {
	t.Parallel()

	headers, diags := customHeadersFromConfig(context.Background(), customHeadersMap(t, map[string]string{
		"Example-Header": "secret",
	}))
	if diags.HasError() {
		t.Fatalf("customHeadersFromConfig returned diagnostics: %s", diagnosticsString(diags))
	}
	if got := headers.Get("Example-Header"); got != "secret" {
		t.Fatalf("Example-Header = %q, want secret", got)
	}
}

func TestCustomHeadersFromConfigTreatsNullAsEmpty(t *testing.T) {
	t.Parallel()

	headers, diags := customHeadersFromConfig(context.Background(), types.MapNull(types.StringType))
	if diags.HasError() {
		t.Fatalf("customHeadersFromConfig returned diagnostics: %s", diagnosticsString(diags))
	}
	if len(headers) != 0 {
		t.Fatalf("headers = %v, want empty", headers)
	}
}

func TestCustomHeadersFromConfigRejectsInvalidHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "reserved header, case-insensitively",
			headers: map[string]string{"authorization": "secret"},
			want:    "Reserved custom header",
		},
		{
			name:    "empty header name",
			headers: map[string]string{"": "secret"},
			want:    "must not be empty",
		},
		{
			name:    "invalid header name",
			headers: map[string]string{"Invalid Header": "secret"},
			want:    "not a valid HTTP header name",
		},
		{
			name:    "empty header value",
			headers: map[string]string{"Example-Header": ""},
			want:    "must not have an empty value",
		},
		{
			name:    "invalid header value",
			headers: map[string]string{"Example-Header": "bad\nvalue"},
			want:    "not a valid HTTP header value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, diags := customHeadersFromConfig(context.Background(), customHeadersMap(t, tt.headers))
			if !diags.HasError() {
				t.Fatalf("expected error diagnostics containing %q, got none", tt.want)
			}
			if got := diagnosticsString(diags); !strings.Contains(got, tt.want) {
				t.Fatalf("diagnostics = %q, want substring %q", got, tt.want)
			}
		})
	}
}

func TestCustomHeadersSchemaIsSensitive(t *testing.T) {
	t.Parallel()

	var resp fwprovider.SchemaResponse
	(&LogfireProvider{}).Schema(context.Background(), fwprovider.SchemaRequest{}, &resp)

	attribute, ok := resp.Schema.Attributes["custom_headers"]
	if !ok {
		t.Fatal("custom_headers schema attribute is missing")
	}

	customHeaders, ok := attribute.(schema.MapAttribute)
	if !ok {
		t.Fatalf("custom_headers schema attribute = %T, want schema.MapAttribute", attribute)
	}
	if !customHeaders.Sensitive {
		t.Fatal("custom_headers schema attribute is not sensitive")
	}
	if !customHeaders.Optional {
		t.Fatal("custom_headers schema attribute is not optional")
	}
	if !customHeaders.ElementType.Equal(types.StringType) {
		t.Fatalf("custom_headers element type = %s, want %s", customHeaders.ElementType.String(), types.StringType.String())
	}
}

func customHeadersMap(t *testing.T, values map[string]string) types.Map {
	t.Helper()

	elements := make(map[string]attr.Value, len(values))
	for name, value := range values {
		elements[name] = types.StringValue(value)
	}
	return types.MapValueMust(types.StringType, elements)
}

func diagnosticsString(diags diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(d.Summary())
		b.WriteString(": ")
		b.WriteString(d.Detail())
	}
	return b.String()
}
